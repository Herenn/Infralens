package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// These tests drive every provider against httptest servers that mimic the
// documented request/response shapes. No real provider API is contacted.

func docMessages() []Message {
	return []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
}

func decodeJSONBody(t *testing.T, r *http.Request) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decoding request body: %v", err)
	}
	return m
}

// --- defaults ------------------------------------------------------------

func TestDefaultModels(t *testing.T) {
	m := NewManager(&Config{OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	want := map[Provider]string{
		ProviderOpenAI:    "gpt-6-luna",
		ProviderAnthropic: "claude-haiku-5-5",
		ProviderGemini:    "gemini-3.5-flash-lite",
		ProviderOllama:    "", // resolved from installed models at request time
		ProviderLMStudio:  "",
	}
	for p, w := range want {
		prov, _ := m.GetProvider(p)
		if got := prov.DefaultModel(); got != w {
			t.Errorf("%s default model = %q, want %q", p, got, w)
		}
	}
	// Configured values win.
	m = NewManager(&Config{AnthropicModel: "claude-sonnet-5-5"})
	prov, _ := m.GetProvider(ProviderAnthropic)
	if got := prov.DefaultModel(); got != "claude-sonnet-5-5" {
		t.Errorf("configured model not used, got %q", got)
	}
}

// --- OpenAI --------------------------------------------------------------

func TestOpenAICompleteRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		body := decodeJSONBody(t, r)
		if body["model"] != "gpt-6-luna" {
			t.Errorf("model = %v", body["model"])
		}
		if _, ok := body["temperature"]; ok {
			t.Error("temperature must not be sent to OpenAI (reasoning models reject non-default values)")
		}
		if _, ok := body["max_tokens"]; ok {
			t.Error("deprecated max_tokens must not be sent to OpenAI")
		}
		if got := body["max_completion_tokens"]; got != float64(500+openAIReasoningHeadroom) {
			t.Errorf("max_completion_tokens = %v", got)
		}
		if got := body["reasoning_effort"]; got != "low" {
			t.Errorf("reasoning_effort = %v, want low", got)
		}
		io.WriteString(w, `{"model":"gpt-6-luna","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(&Config{OpenAIAPIKey: "sk-test"})
	p.baseURL = srv.URL
	resp, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages(), MaxTokens: 500, Temperature: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || resp.TokensUsed != 7 {
		t.Errorf("unexpected response %+v", resp)
	}
}

func TestOpenAIModelNotFoundIsModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"The model `+"`gpt-3.5-turbo`"+` does not exist or you do not have access to it.","type":"invalid_request_error","code":"model_not_found"}}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(&Config{OpenAIAPIKey: "sk-test", OpenAIModel: "gpt-3.5-turbo"})
	p.baseURL = srv.URL
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	var me *ModelError
	if !errors.As(err, &me) || !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("want *ModelError, got %T %v", err, err)
	}
	if me.Provider != "openai" || me.Model != "gpt-3.5-turbo" {
		t.Errorf("unexpected %+v", me)
	}
	if !strings.Contains(err.Error(), "Pick another model") || !strings.Contains(err.Error(), "OPENAI_MODEL") {
		t.Errorf("error text not actionable: %q", err.Error())
	}
}

func TestOpenAIAuthErrorIsRedacted(t *testing.T) {
	leaked := "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+leaked+`.","type":"invalid_request_error","code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(&Config{OpenAIAPIKey: "sk-test"})
	p.baseURL = srv.URL
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("want 401 *APIError, got %T %v", err, err)
	}
	if errors.Is(err, ErrModelUnavailable) {
		t.Error("auth error misclassified as model error")
	}
	if strings.Contains(err.Error(), leaked) {
		t.Errorf("upstream-echoed key not redacted: %q", err.Error())
	}
}

func TestOpenAIListModelsFiltersAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"object":"list","data":[
			{"id":"gpt-6-luna","created":300},
			{"id":"text-embedding-3-small","created":400},
			{"id":"gpt-6.1-sol","created":500},
			{"id":"gpt-4o-mini-tts","created":450},
			{"id":"dall-e-3","created":100},
			{"id":"gpt-3.5-turbo-instruct","created":50},
			{"id":"o4-mini","created":200},
			{"id":"whisper-1","created":10},
			{"id":"gpt-6-codex","created":600},
			{"id":"omni-moderation-latest","created":20}
		]}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(&Config{OpenAIAPIKey: "sk-test"})
	p.baseURL = srv.URL
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gpt-6.1-sol", "gpt-6-luna", "o4-mini"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- Anthropic -----------------------------------------------------------

func TestAnthropicCompleteRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if v := r.Header.Get("anthropic-version"); v != "2026-10-21" {
			t.Errorf("anthropic-version = %q", v)
		}
		body := decodeJSONBody(t, r)
		for _, banned := range []string{"temperature", "top_p", "top_k"} {
			if _, ok := body[banned]; ok {
				t.Errorf("%s must not be sent (400 on API version 2026-10-21)", banned)
			}
		}
		if body["model"] != "claude-haiku-5-5" || body["system"] != "sys" {
			t.Errorf("model/system = %v / %v", body["model"], body["system"])
		}
		if oc, _ := body["output_config"].(map[string]interface{}); oc["effort"] != "low" {
			t.Errorf("output_config = %v, want effort low", body["output_config"])
		}
		if got := body["max_tokens"]; got != float64(2500+anthropicReasoningHeadroom) {
			t.Errorf("max_tokens = %v", got)
		}
		// A thinking block precedes the text; only text must be returned.
		io.WriteString(w, `{"model":"claude-haiku-5-5","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"docs"}],"usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(&Config{AnthropicAPIKey: "sk-ant-test"})
	p.baseURL = srv.URL
	resp, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages(), MaxTokens: 2500, Temperature: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "docs" || resp.TokensUsed != 7 {
		t.Errorf("unexpected response %+v", resp)
	}
}

func TestAnthropicRetiredModelIsModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-3-haiku-20240307"}}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(&Config{AnthropicAPIKey: "k", AnthropicModel: "claude-3-haiku-20240307"})
	p.baseURL = srv.URL
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	var me *ModelError
	if !errors.As(err, &me) || me.Model != "claude-3-haiku-20240307" || me.Provider != "anthropic" {
		t.Fatalf("want *ModelError for the retired model, got %T %v", err, err)
	}
}

func TestAnthropicEmptyAnswerAtMaxTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"model":"claude-haiku-5-5","stop_reason":"max_tokens","content":[{"type":"thinking","thinking":"..."}]}`)
	}))
	defer srv.Close()
	p := NewAnthropicProvider(&Config{AnthropicAPIKey: "k"})
	p.baseURL = srv.URL
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	if err == nil || !strings.Contains(err.Error(), "output budget") {
		t.Fatalf("want output-budget error, got %v", err)
	}
}

func TestAnthropicListModelsPaginatesAndSkipsDeprecated(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing auth/version headers")
		}
		if r.URL.Query().Get("after_id") == "" {
			io.WriteString(w, `{"data":[{"id":"claude-fable-5-2","lifecycle":"active"},{"id":"claude-sonnet-4-5-20250929","lifecycle":"deprecated"}],"has_more":true,"last_id":"claude-sonnet-4-5-20250929"}`)
			return
		}
		if got := r.URL.Query().Get("after_id"); got != "claude-sonnet-4-5-20250929" {
			t.Errorf("after_id = %q", got)
		}
		io.WriteString(w, `{"data":[{"id":"claude-haiku-5-5","lifecycle":"active"}],"has_more":false,"last_id":"claude-haiku-5-5"}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider(&Config{AnthropicAPIKey: "k"})
	p.baseURL = srv.URL
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-fable-5-2", "claude-haiku-5-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if calls != 2 {
		t.Errorf("expected 2 page requests, got %d", calls)
	}
}

// --- Gemini --------------------------------------------------------------

func TestGeminiKeyInHeaderNotURL(t *testing.T) {
	key := "AIza" + "SyD-this-would-be-a-live-secret-key-value" // split: see transport_test.go
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), key) || r.URL.Query().Get("key") != "" {
			t.Errorf("API key must not be in the URL: %s", r.URL)
		}
		if r.Header.Get("x-goog-api-key") != key {
			t.Errorf("x-goog-api-key header missing")
		}
		if r.URL.Path != "/models/gemini-3.5-flash-lite:generateContent" {
			t.Errorf("path = %s", r.URL.Path)
		}
		body := decodeJSONBody(t, r)
		si, _ := body["systemInstruction"].(map[string]interface{})
		if si == nil {
			t.Error("system prompt must go in systemInstruction")
		}
		gc, _ := body["generationConfig"].(map[string]interface{})
		if _, ok := gc["temperature"]; ok {
			t.Error("temperature must not be sent to Gemini")
		}
		io.WriteString(w, `{"modelVersion":"gemini-3.5-flash-lite","candidates":[{"content":{"parts":[{"text":"thinking...","thought":true},{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":9}}`)
	}))
	defer srv.Close()

	p := NewGeminiProvider(&Config{GeminiAPIKey: key})
	p.baseURL = srv.URL
	resp, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "answer" || resp.Model != "gemini-3.5-flash-lite" {
		t.Errorf("unexpected response %+v", resp)
	}
}

func TestGeminiModelNameIsPathEscaped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("model name injected a query string: %q", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":404,"message":"models/gemini-pro?alt=x is not found for API version v1beta","status":"NOT_FOUND"}}`)
	}))
	defer srv.Close()

	p := NewGeminiProvider(&Config{GeminiAPIKey: "k", GeminiModel: "gemini-pro?alt=x"})
	p.baseURL = srv.URL
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("want model error, got %v", err)
	}
}

func TestGeminiListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" || r.URL.Query().Get("key") != "" {
			t.Errorf("key must be sent as header only")
		}
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"models":[
				{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent","countTokens"]},
				{"name":"models/gemini-embedding-2","supportedGenerationMethods":["embedContent"]},
				{"name":"models/gemini-3.1-flash-image","supportedGenerationMethods":["generateContent"]}
			],"nextPageToken":"p2"}`)
			return
		}
		io.WriteString(w, `{"models":[
			{"name":"models/gemini-3.5-flash-lite","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-flash-tts","supportedGenerationMethods":["generateContent"]},
			{"name":"models/imagen-4.0-generate","supportedGenerationMethods":["predict"]}
		]}`)
	}))
	defer srv.Close()

	p := NewGeminiProvider(&Config{GeminiAPIKey: "k"})
	p.baseURL = srv.URL
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gemini-3.8-flash", "gemini-3.5-flash-lite"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- Ollama / LM Studio --------------------------------------------------

func newOllamaServer(t *testing.T, tags string, chat http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, tags)
		case "/api/chat":
			chat(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaUsesFirstInstalledModelWhenUnset(t *testing.T) {
	srv := newOllamaServer(t,
		`{"models":[{"name":"nomic-embed-text:latest"},{"name":"qwen3.5:4b"},{"name":"gemma4:e4b"}]}`,
		func(w http.ResponseWriter, r *http.Request) {
			body := decodeJSONBody(t, r)
			if body["model"] != "qwen3.5:4b" {
				t.Errorf("model = %v, want first non-embedding installed model", body["model"])
			}
			io.WriteString(w, `{"model":"qwen3.5:4b","message":{"role":"assistant","content":"hi"},"done":true}`)
		})

	p := NewOllamaProvider(&Config{OllamaURL: srv.URL})
	resp, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hi" {
		t.Errorf("content = %q", resp.Content)
	}
}

func TestOllamaNoModelsInstalled(t *testing.T) {
	srv := newOllamaServer(t, `{"models":[]}`, func(w http.ResponseWriter, r *http.Request) {
		t.Error("chat must not be called with no model")
	})
	p := NewOllamaProvider(&Config{OllamaURL: srv.URL})
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	if err == nil || !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("want actionable 'ollama pull' error, got %v", err)
	}
}

func TestOllamaModelNotPulledIsModelError(t *testing.T) {
	srv := newOllamaServer(t, `{"models":[{"name":"gemma4:e4b"}]}`, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"model \"llama2\" not found, try pulling it first"}`)
	})
	p := NewOllamaProvider(&Config{OllamaURL: srv.URL, OllamaModel: "llama2"})
	_, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	var me *ModelError
	if !errors.As(err, &me) || me.Model != "llama2" {
		t.Fatalf("want *ModelError for llama2, got %T %v", err, err)
	}
}

func TestOllamaListModelsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	p := NewOllamaProvider(&Config{OllamaURL: srv.URL})
	if _, err := p.ListModels(context.Background()); err == nil {
		t.Fatal("expected an error for a non-200 tags response")
	}
}

func TestLMStudioListModelsFiltersEmbeddings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"text-embedding-nomic-embed-text-v1.5"},{"id":"qwen3.5-9b-instruct"}]}`)
	}))
	defer srv.Close()
	p := NewLMStudioProvider(&Config{LMStudioURL: srv.URL})
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"qwen3.5-9b-instruct"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- Manager.Models: caching, fallback, timeouts -------------------------

func managerWithOpenAI(t *testing.T, url string) *Manager {
	t.Helper()
	m := NewManager(&Config{OpenAIAPIKey: "sk-test", OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	m.providers[ProviderOpenAI].(*OpenAIProvider).baseURL = url
	return m
}

func TestManagerModelsLiveAndCached(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		io.WriteString(w, `{"data":[{"id":"gpt-6-luna","created":1}]}`)
	}))
	defer srv.Close()

	m := managerWithOpenAI(t, srv.URL)
	for i := 0; i < 3; i++ {
		cat, err := m.Models(context.Background(), ProviderOpenAI)
		if err != nil {
			t.Fatal(err)
		}
		if cat.Source != ModelSourceLive || !reflect.DeepEqual(cat.Models, []string{"gpt-6-luna"}) || cat.DefaultModel != "gpt-6-luna" {
			t.Fatalf("unexpected catalogue %+v", cat)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 upstream call thanks to caching, got %d", calls)
	}

	// Expiry: advance the cache clock past the TTL.
	m.models.now = func() time.Time { return time.Now().Add(modelCacheTTL + time.Second) }
	if _, err := m.Models(context.Background(), ProviderOpenAI); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected a refetch after TTL, got %d calls", calls)
	}
}

func TestManagerModelsFallbackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}))
	defer srv.Close()

	m := managerWithOpenAI(t, srv.URL)
	cat, err := m.Models(context.Background(), ProviderOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Source != ModelSourceFallback || !reflect.DeepEqual(cat.Models, FallbackModels(ProviderOpenAI)) {
		t.Errorf("want fallback list, got %+v", cat)
	}
	if !strings.Contains(cat.Error, "upstream exploded") {
		t.Errorf("listing error not reported: %q", cat.Error)
	}
}

func TestManagerModelsTimeoutFallsBack(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	m := managerWithOpenAI(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	cat, err := m.Models(ctx, ProviderOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("listing did not honour the context deadline")
	}
	if cat.Source != ModelSourceFallback {
		t.Errorf("want fallback after timeout, got %+v", cat)
	}
	// A result produced because the caller's context ended is not cached.
	if _, ok := m.models.get(ProviderOpenAI); ok {
		t.Error("timed-out listing must not be cached")
	}
}

func TestManagerModelsUnconfiguredMakesNoCall(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	m := NewManager(&Config{OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	m.providers[ProviderAnthropic].(*AnthropicProvider).baseURL = srv.URL
	cat, err := m.Models(context.Background(), ProviderAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Error("an unconfigured cloud provider must not be called")
	}
	if cat.Configured || cat.Source != ModelSourceFallback || cat.DefaultModel != DefaultAnthropicModel {
		t.Errorf("unexpected %+v", cat)
	}
}

func TestManagerModelsLocalDefaultIsFirstInstalled(t *testing.T) {
	srv := newOllamaServer(t, `{"models":[{"name":"gemma4:e4b"},{"name":"qwen3.5:4b"}]}`, nil)
	m := NewManager(&Config{OllamaURL: srv.URL, LMStudioURL: "http://127.0.0.1:1"})
	cat, err := m.Models(context.Background(), ProviderOllama)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Source != ModelSourceLive || cat.DefaultModel != "gemma4:e4b" {
		t.Errorf("unexpected %+v", cat)
	}

	// Ollama down: fallback suggestions, and a named default to pull.
	m = NewManager(&Config{OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	cat, _ = m.Models(context.Background(), ProviderOllama)
	if cat.Source != ModelSourceFallback || cat.DefaultModel != DefaultOllamaModel {
		t.Errorf("unexpected %+v", cat)
	}
}

func TestUpdateConfigResetsModelCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer srv.Close()
	m := managerWithOpenAI(t, srv.URL)
	if _, err := m.Models(context.Background(), ProviderOpenAI); err != nil {
		t.Fatal(err)
	}
	m.UpdateConfig(&Config{OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	cat, _ := m.Models(context.Background(), ProviderOpenAI)
	if cat.Source != ModelSourceFallback || cat.Configured {
		t.Errorf("catalogue from the old credentials survived UpdateConfig: %+v", cat)
	}
}

func TestSuggestModelsExcludesRejected(t *testing.T) {
	m := NewManager(&Config{OllamaURL: "http://127.0.0.1:1", LMStudioURL: "http://127.0.0.1:1"})
	got := m.SuggestModels(context.Background(), ProviderAnthropic, "claude-haiku-5-5", 2)
	if want := []string{"claude-sonnet-5-5", "claude-opus-5-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// --- transport hardening -------------------------------------------------

// TestCloudClientDoesNotFollowRedirects: Go forwards custom headers such as
// x-api-key / x-goog-api-key across a cross-host redirect, so following one
// could leak a key. The cloud client must stop at the 3xx.
func TestCloudClientDoesNotFollowRedirects(t *testing.T) {
	var leaked int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("x-goog-api-key") != "" {
			atomic.AddInt32(&leaked, 1)
		}
		io.WriteString(w, `{}`)
	}))
	defer other.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	a := NewAnthropicProvider(&Config{AnthropicAPIKey: "sk-ant-secret"})
	a.baseURL = redirector.URL
	_, errA := a.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
	g := NewGeminiProvider(&Config{GeminiAPIKey: "AIza-secret"})
	g.baseURL = redirector.URL
	_, errG := g.ListModels(context.Background())

	if leaked != 0 {
		t.Fatalf("API key was forwarded to the redirect target %d time(s)", leaked)
	}
	if errA == nil || errG == nil {
		t.Errorf("expected errors for redirected requests, got %v / %v", errA, errG)
	}
}

func TestListModelsTransportErrorIsSanitized(t *testing.T) {
	g := NewGeminiProvider(&Config{GeminiAPIKey: "AIza" + "SyD-this-would-be-a-live-secret-key-value"})
	g.baseURL = "http://127.0.0.1:1/v1beta"
	_, err := g.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "127.0.0.1:1/v1beta/models") {
		t.Errorf("request URL leaked into error: %q", err.Error())
	}
}

// --- error classification ------------------------------------------------

func TestDecodeErrorFields(t *testing.T) {
	cases := []struct {
		body, code, msg string
	}{
		{`{"error":{"message":"m","code":"model_not_found","type":"invalid_request_error"}}`, "model_not_found", "m"},
		{`{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`, "not_found_error", "model: x"},
		{`{"error":{"code":404,"message":"models/x is not found","status":"NOT_FOUND"}}`, "NOT_FOUND", "models/x is not found"},
		{`{"error":"model \"x\" not found"}`, "", `model "x" not found`},
		{`<html>bad gateway</html>`, "", ""},
	}
	for _, c := range cases {
		code, msg := decodeErrorFields([]byte(c.body))
		if code != c.code || msg != c.msg {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.body, code, msg, c.code, c.msg)
		}
	}
}

type rejectionCase struct {
	model     string
	status    int
	code, msg string
}

func TestLooksLikeModelRejection(t *testing.T) {
	yes := []rejectionCase{
		{"llama2", 404, "", `model "llama2" not found, try pulling it first`},
		{"claude-3-haiku-20240307", 404, "not_found_error", "model: claude-3-haiku-20240307"},
		{"gemini-x", 400, "", "* GenerateContentRequest.model: model gemini-x is not supported for generateContent"},
		{"gpt-4-0613", 400, "", "The model gpt-4-0613 has been deprecated"},
		{"gpt-3.5-turbo", 404, "model_not_found", "The model `gpt-3.5-turbo` does not exist"},
	}
	for _, c := range yes {
		if !looksLikeModelRejection(c.model, c.status, c.code, c.msg) {
			t.Errorf("expected model rejection for %+v", c)
		}
	}
	no := []rejectionCase{
		{"", 404, "", "Not Found"},                                         // list-models call: no model
		{"gpt-6-luna", 404, "", "404 page not found"},                      // wrong base URL, model not named
		{"gpt-6-luna", 401, "invalid_api_key", "Incorrect API key"},        // auth
		{"gpt-6-luna", 429, "", "Rate limit reached for model gpt-6-luna"}, // quota
		{"gpt-6-luna", 429, "insufficient_quota", "You exceeded your current quota for model gpt-6-luna, which has been deprecated for free tier"},
		{"gpt-6-luna", 400, "", "Unsupported parameter: 'max_tokens' has been deprecated for model gpt-6-luna; use max_completion_tokens"},
		{"gpt-6-luna", 400, "", "max_tokens: must be at most 128000"},
		{"gpt-6-luna", 503, "", "model gpt-6-luna not found in this region"}, // server-side
	}
	for _, c := range no {
		if looksLikeModelRejection(c.model, c.status, c.code, c.msg) {
			t.Errorf("unexpected model rejection for %+v", c)
		}
	}
}

// TestListModels404IsNotModelError: a list-models call names no model, so a
// 404 from it must not become "<provider> rejected model \"\"".
func TestListModels404IsNotModelError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	lm := NewLMStudioProvider(&Config{LMStudioURL: srv.URL})
	_, err := lm.ListModels(context.Background())
	if err == nil || errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("want a plain API error, got %T %v", err, err)
	}
	o := NewOpenAIProvider(&Config{OpenAIAPIKey: "k"})
	o.baseURL = srv.URL
	if _, err := o.ListModels(context.Background()); err == nil || errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("want a plain API error, got %T %v", err, err)
	}
}

// --- refusals, blocks and empty answers ----------------------------------

func TestRefusedOrEmptyAnswersAreErrors(t *testing.T) {
	cases := []struct {
		name, provider, body, wantReason string
	}{
		{"openai refusal", "openai",
			`{"model":"gpt-6-luna","choices":[{"message":{"role":"assistant","content":null,"refusal":"I can't help with that."},"finish_reason":"stop"}]}`,
			"refusal"},
		{"openai content_filter", "openai",
			`{"model":"gpt-6-luna","choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"content_filter"}]}`,
			"content_filter"},
		{"openai empty", "openai",
			`{"model":"gpt-6-luna","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
			"empty response"},
		{"anthropic refusal with partial text", "anthropic",
			`{"model":"claude-haiku-5-5","stop_reason":"refusal","content":[{"type":"text","text":"Here is the start of"}]}`,
			"refusal"},
		{"anthropic empty", "anthropic",
			`{"model":"claude-haiku-5-5","stop_reason":"end_turn","content":[]}`,
			"empty response"},
		{"gemini prompt blocked", "gemini",
			`{"promptFeedback":{"blockReason":"SAFETY"}}`,
			"prompt blocked: SAFETY"},
		{"gemini output safety", "gemini",
			`{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"SAFETY"}]}`,
			"output withheld: SAFETY"},
		{"gemini no candidates", "gemini", `{}`, "no candidates"},
		{"lmstudio empty", "lmstudio",
			`{"model":"m","choices":[{"message":{"role":"assistant","content":"  "},"finish_reason":"stop"}]}`,
			"empty response"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, c.body)
			}))
			defer srv.Close()

			var prov LLMProvider
			switch c.provider {
			case "openai":
				p := NewOpenAIProvider(&Config{OpenAIAPIKey: "k"})
				p.baseURL = srv.URL
				prov = p
			case "anthropic":
				p := NewAnthropicProvider(&Config{AnthropicAPIKey: "k"})
				p.baseURL = srv.URL
				prov = p
			case "gemini":
				p := NewGeminiProvider(&Config{GeminiAPIKey: "k"})
				p.baseURL = srv.URL
				prov = p
			case "lmstudio":
				prov = NewLMStudioProvider(&Config{LMStudioURL: srv.URL, LMStudioModel: "m"})
			}
			resp, err := prov.Complete(context.Background(), CompletionRequest{Messages: docMessages()})
			var re *ResponseRefusedError
			if !errors.As(err, &re) {
				t.Fatalf("want *ResponseRefusedError, got resp=%+v err=%v", resp, err)
			}
			if !strings.Contains(re.Reason, c.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", re.Reason, c.wantReason)
			}
		})
	}
}

// --- effort / thinking gating --------------------------------------------

func TestReasoningControlsOnlySentToSupportingModels(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-6-luna": true, "gpt-6.1-sol": true, "gpt-6-astra": true, "gpt-5.6-terra": true, "o4-mini": true,
		"gpt-4o": false, "gpt-4.1-mini": false, "gpt-5-chat-latest": false,
	} {
		if got := openAIAcceptsReasoningEffort(model); got != want {
			t.Errorf("openAIAcceptsReasoningEffort(%q) = %v, want %v", model, got, want)
		}
	}
	for model, want := range map[string]bool{
		"claude-haiku-5-5": true, "claude-fable-5-2": true, "claude-opus-4-6": true, "claude-sonnet-5-6": true,
		"claude-haiku-4-5-20251001": false, "claude-3-haiku-20240307": false,
	} {
		if got := anthropicAcceptsEffort(model); got != want {
			t.Errorf("anthropicAcceptsEffort(%q) = %v, want %v", model, got, want)
		}
	}
	for model, want := range map[string]string{
		"gemini-3.8-flash": "low", "gemini-3.6-flash": "low", "models/gemini-3.1-pro-preview": "low",
		"gemini-3.5-flash-lite": "", "gemini-2.5-flash": "", "gemini-4-argon": "",
	} {
		if got := geminiThinkingLevel(model); got != want {
			t.Errorf("geminiThinkingLevel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestOpenAIOmitsReasoningEffortForNonReasoningModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := decodeJSONBody(t, r)["reasoning_effort"]; ok {
			t.Error("reasoning_effort must not be sent to gpt-4o")
		}
		io.WriteString(w, `{"model":"gpt-4o","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	p := NewOpenAIProvider(&Config{OpenAIAPIKey: "k", OpenAIModel: "gpt-4o"})
	p.baseURL = srv.URL
	if _, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()}); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiSendsThinkingLevelForFlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gc, _ := decodeJSONBody(t, r)["generationConfig"].(map[string]interface{})
		tc, _ := gc["thinkingConfig"].(map[string]interface{})
		if tc["thinkingLevel"] != "low" {
			t.Errorf("thinkingConfig = %v", gc["thinkingConfig"])
		}
		if gc["maxOutputTokens"] != float64(2048+geminiReasoningHeadroom) {
			t.Errorf("maxOutputTokens = %v", gc["maxOutputTokens"])
		}
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()
	p := NewGeminiProvider(&Config{GeminiAPIKey: "k", GeminiModel: "gemini-3.8-flash"})
	p.baseURL = srv.URL
	if _, err := p.Complete(context.Background(), CompletionRequest{Messages: docMessages()}); err != nil {
		t.Fatal(err)
	}
}

// --- cold-cache coalescing -----------------------------------------------

func TestManagerModelsCoalescesConcurrentColdCalls(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(100 * time.Millisecond) // keep the call in flight while others arrive
		io.WriteString(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer srv.Close()

	m := managerWithOpenAI(t, srv.URL)
	const n = 20
	var wg sync.WaitGroup
	results := make([]ModelCatalog, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = m.Models(context.Background(), ProviderOpenAI)
		}(i)
	}
	wg.Wait()

	if calls != 1 {
		t.Errorf("%d concurrent cold calls made %d upstream requests, want 1", n, calls)
	}
	for i, r := range results {
		if r.Source != ModelSourceLive {
			t.Errorf("caller %d got %+v", i, r)
		}
	}
}

// --- redaction -----------------------------------------------------------

func TestRedactsCurrentOpenAIKeyFormats(t *testing.T) {
	for _, key := range []string{
		"sk-proj-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcd",
		"sk-svcacct-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"sk-admin-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
	} {
		out := redactSecrets("Incorrect API key provided: " + key + ".")
		if strings.Contains(out, "AbCdEfGhIjKl") {
			t.Errorf("key not redacted: %q", out)
		}
	}
}

func TestUpstreamErrorRedactsBeforeTruncating(t *testing.T) {
	key := "sk-proj-" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"
	// Place the key so it straddles the maxErrorDetail cut.
	msg := strings.Repeat("x", maxErrorDetail-10) + " " + key + " tail"
	err := upstreamError("openai", "", 401, "", msg, nil)
	if strings.Contains(err.Error(), "sk-proj") {
		t.Errorf("key prefix survived truncation: %q", err.Error())
	}
}

func TestCleanDetailTruncatesOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("a", maxErrorDetail-1) + "é" + "more"
	got := cleanDetail(s)
	if !utf8.ValidString(got) {
		t.Errorf("truncation split a UTF-8 rune: %q", got[len(got)-8:])
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncation marker, got suffix %q", got[len(got)-5:])
	}
}
