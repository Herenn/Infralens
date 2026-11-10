package llm

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default model per provider, used when the corresponding *_MODEL setting is
// empty. Verified against each provider's model documentation on 2026-11-11;
// see the comment on each value. Cheap, fast models are chosen deliberately:
// service docs are a short, well-specified summarisation task.
const (
	// OpenAI "GPT-6 Luna": "most efficient model for focused, high-volume
	// tasks", $0.10/$0.50 per MTok (developers.openai.com/api/docs/models).
	DefaultOpenAIModel = "gpt-6-luna"
	// Anthropic Claude Haiku 5.5, active, retirement not before 2027-10-07
	// (platform.claude.com/docs/en/about-claude/models/overview).
	DefaultAnthropicModel = "claude-haiku-5-5"
	// Gemini 3.5 Flash-Lite, Stable: "fastest, most cost-effective 3.5 model"
	// (aistudio.google.com/docs/models).
	DefaultGeminiModel = "gemini-3.5-flash-lite"
	// Ollama: only used when OLLAMA_MODEL is empty AND no local model could be
	// listed; normally the first installed model is used instead.
	DefaultOllamaModel = "gemma4"
)

// fallbackModels is the short static list shown when a provider's list-models
// API cannot be reached (no credentials yet, network failure, timeout). It is
// intentionally small: the live list is the source of truth.
var fallbackModels = map[Provider][]string{
	ProviderOpenAI:    {"gpt-6-luna", "gpt-6.1-terra", "gpt-6.1-sol", "gpt-6-astra"},
	ProviderAnthropic: {"claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5", "claude-fable-5-2"},
	ProviderGemini:    {"gemini-3.5-flash-lite", "gemini-3.6-flash", "gemini-3.8-flash"},
	// Suggestions to `ollama pull`; the real list is whatever is installed.
	ProviderOllama:   {"gemma4", "qwen3.5", "llama3.2"},
	ProviderLMStudio: {},
}

// FallbackModels returns a copy of the static fallback list for p.
func FallbackModels(p Provider) []string {
	return append([]string(nil), fallbackModels[p]...)
}

// Model catalogue cache settings. Successful listings are cached for
// modelCacheTTL; failures for the much shorter modelErrorTTL so a transient
// outage does not pin the fallback list for long, while a UI repeatedly
// opening the settings panel cannot hammer a provider that is down.
const (
	modelCacheTTL    = 15 * time.Minute
	modelErrorTTL    = time.Minute
	modelListTimeout = 8 * time.Second
)

// Model catalogue sources.
const (
	ModelSourceLive     = "live"
	ModelSourceFallback = "fallback"
)

// ModelCatalog is the model list for one provider as served to the UI.
type ModelCatalog struct {
	Provider     string    `json:"provider"`
	Configured   bool      `json:"configured"`
	DefaultModel string    `json:"default_model"`
	Models       []string  `json:"models"`
	Source       string    `json:"source"`          // "live" or "fallback"
	Error        string    `json:"error,omitempty"` // why the live listing failed, redacted
	FetchedAt    time.Time `json:"fetched_at"`
}

type cachedCatalog struct {
	catalog ModelCatalog
	expires time.Time
}

// modelCache holds per-provider catalogues. It belongs to a Manager and is
// replaced wholesale by UpdateConfig, since new credentials can change what a
// provider lists.
type modelCache struct {
	mu       sync.Mutex
	entries  map[Provider]cachedCatalog
	inflight map[Provider]*modelCall
	now      func() time.Time
}

func newModelCache() *modelCache {
	return &modelCache{
		entries:  make(map[Provider]cachedCatalog),
		inflight: make(map[Provider]*modelCall),
		now:      time.Now,
	}
}

func (c *modelCache) get(p Provider) (ModelCatalog, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(p)
}

// getLocked returns an unexpired entry. Callers must hold c.mu.
func (c *modelCache) getLocked(p Provider) (ModelCatalog, bool) {
	e, ok := c.entries[p]
	if !ok || c.now().After(e.expires) {
		return ModelCatalog{}, false
	}
	return e.catalog, true
}

// Models returns the model catalogue for provider p: the provider's live
// list-models answer when it is configured and reachable, otherwise the
// static fallback list. It never returns an error for a known provider -
// listing failures are reported in ModelCatalog.Error - so the UI always has
// something to show.
func (m *Manager) Models(ctx context.Context, p Provider) (ModelCatalog, error) {
	m.mu.RLock()
	provider, err := m.getProviderLocked(p)
	cache := m.models
	m.mu.RUnlock()
	if err != nil {
		return ModelCatalog{}, err
	}

	// Fast path, then join or start the single in-flight listing for p so a
	// burst of requests on a cold cache makes one upstream call, not N.
	cache.mu.Lock()
	if cat, ok := cache.getLocked(p); ok {
		cache.mu.Unlock()
		return cat, nil
	}
	call, running := cache.inflight[p]
	if !running {
		call = &modelCall{done: make(chan struct{})}
		cache.inflight[p] = call
		go func() {
			cat, ttl := fetchCatalog(provider, p, cache.now())
			cache.mu.Lock()
			cache.entries[p] = cachedCatalog{catalog: cat, expires: cache.now().Add(ttl)}
			delete(cache.inflight, p)
			cache.mu.Unlock()
			call.cat = cat
			close(call.done)
		}()
	}
	cache.mu.Unlock()

	select {
	case <-call.done:
		return call.cat, nil
	case <-ctx.Done():
		// This caller gave up; the shared listing keeps running (bounded by
		// modelListTimeout) and will populate the cache for the next request.
		return ModelCatalog{
			Provider:     string(p),
			DefaultModel: provider.DefaultModel(),
			Models:       FallbackModels(p),
			Source:       ModelSourceFallback,
			Error:        "model listing did not finish in time",
			FetchedAt:    cache.now(),
		}, nil
	}
}

// modelCall is one in-flight listing shared by concurrent Models callers.
type modelCall struct {
	done chan struct{}
	cat  ModelCatalog // valid once done is closed
}

// fetchCatalog performs the listing for p and returns the catalogue and how
// long to cache it. It runs detached from any one request's context (with
// its own modelListTimeout) because its result is shared by every caller
// waiting on it.
func fetchCatalog(provider LLMProvider, p Provider, now time.Time) (ModelCatalog, time.Duration) {
	cat := ModelCatalog{
		Provider:     string(p),
		Configured:   provider.IsConfigured(),
		DefaultModel: provider.DefaultModel(),
		FetchedAt:    now,
	}
	if cat.DefaultModel == "" && p == ProviderOllama {
		// Shown when nothing is installed/reachable; replaced below by the
		// first installed model when the live listing succeeds.
		cat.DefaultModel = DefaultOllamaModel
	}

	// Cloud providers cannot list without credentials; skip the call rather
	// than sending an unauthenticated request. Local providers are probed
	// directly - IsConfigured for them is itself a reachability check.
	if !cat.Configured {
		cat.Models = FallbackModels(p)
		cat.Source = ModelSourceFallback
		return cat, modelErrorTTL
	}

	listCtx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
	defer cancel()
	models, listErr := provider.ListModels(listCtx)

	ttl := modelCacheTTL
	switch {
	case listErr != nil:
		cat.Models = FallbackModels(p)
		cat.Source = ModelSourceFallback
		cat.Error = redactSecrets(listErr.Error())
		ttl = modelErrorTTL
	case len(models) == 0:
		cat.Models = FallbackModels(p)
		cat.Source = ModelSourceFallback
		cat.Error = "provider returned no usable models"
		ttl = modelErrorTTL
	default:
		cat.Models = models
		cat.Source = ModelSourceLive
	}
	if provider.DefaultModel() == "" && cat.Source == ModelSourceLive {
		// Local providers with no configured model use the first listed one,
		// matching what Complete does at request time.
		cat.DefaultModel = cat.Models[0]
	}
	return cat, ttl
}

// AllModels returns catalogues for every provider, listing them concurrently
// so one slow provider does not serialise the rest.
func (m *Manager) AllModels(ctx context.Context) []ModelCatalog {
	order := []Provider{ProviderOpenAI, ProviderAnthropic, ProviderGemini, ProviderOllama, ProviderLMStudio}
	out := make([]ModelCatalog, len(order))
	var wg sync.WaitGroup
	for i, p := range order {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			cat, err := m.Models(ctx, p)
			if err != nil {
				cat = ModelCatalog{Provider: string(p), Models: FallbackModels(p), Source: ModelSourceFallback, Error: err.Error()}
			}
			out[i] = cat
		}(i, p)
	}
	wg.Wait()
	return out
}

// SuggestModels returns up to n models from p's catalogue other than exclude,
// for "pick another model" hints after a model was rejected.
func (m *Manager) SuggestModels(ctx context.Context, p Provider, exclude string, n int) []string {
	cat, err := m.Models(ctx, p)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range cat.Models {
		if id == exclude {
			continue
		}
		out = append(out, id)
		if len(out) == n {
			break
		}
	}
	return out
}

// --- per-provider filtering of list-models answers -----------------------

// nonTextModelMarkers identify models in a provider listing that cannot serve
// a plain text chat/completion request (embeddings, speech, images, ...).
var nonTextModelMarkers = []string{
	"embed", "tts", "whisper", "transcribe", "dall-e", "image", "audio", "realtime",
	"moderation", "search", "imagen", "veo", "lyria", "robotics", "computer-use",
	"live", "nano-banana", "aqa", "omni",
}

func isTextModel(id string) bool {
	l := strings.ToLower(id)
	for _, marker := range nonTextModelMarkers {
		if strings.Contains(l, marker) {
			return false
		}
	}
	return true
}

// openAIChatModel keeps OpenAI ids usable with /v1/chat/completions. The
// listing also contains embeddings, speech, image, moderation, legacy
// completions-only ("instruct", "babbage", "davinci") and Responses-only
// ("codex", "-pro", "deep-research") models.
func openAIChatModel(id string) bool {
	l := strings.ToLower(id)
	if !(strings.HasPrefix(l, "gpt-") || strings.HasPrefix(l, "chatgpt-") ||
		(len(l) > 1 && l[0] == 'o' && l[1] >= '0' && l[1] <= '9')) {
		return false
	}
	for _, marker := range []string{"instruct", "codex", "-pro", "deep-research"} {
		if strings.Contains(l, marker) {
			return false
		}
	}
	return isTextModel(l)
}

// sortNewestFirst orders ids by a created timestamp, newest first, ties by id.
func sortNewestFirst(ids []string, created map[string]int64) {
	sort.SliceStable(ids, func(i, j int) bool {
		ci, cj := created[ids[i]], created[ids[j]]
		if ci != cj {
			return ci > cj
		}
		return ids[i] < ids[j]
	})
}
