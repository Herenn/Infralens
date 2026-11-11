package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const anthropicBaseURL = "https://api.anthropic.com/v1"

// anthropicAPIVersion is the anthropic-version header value. 2026-10-21
// removes temperature/top_p/top_k (any value is a 400), which this provider
// no longer sends; nothing else in that version affects the plain-text
// Messages requests made here. See
// platform.claude.com/docs/en/api/versioning.
const anthropicAPIVersion = "2026-10-21"

// anthropicReasoningHeadroom is added to max_tokens. Claude 5.x thinks
// adaptively by default (always on for Opus 5.5 and Fable 5.2) and thinking
// counts toward max_tokens ("Thinking is on by default and counts toward
// max_tokens, so leave room for it" - Haiku 5.5 effort guidance). Paired with
// low effort below, 16K leaves ample room while bounding a runaway request.
const anthropicReasoningHeadroom = 16000

// anthropicEffort is sent as output_config.effort. Anthropic's guidance for
// Haiku 5.5 (default "medium"): "Use low ... for chat, short tool tasks, and
// simple, high-volume requests"; effort "barely changes how long the model's
// replies are", so low cuts thinking cost without shortening the docs.
const anthropicEffort = "low"

// anthropicEffortModels are the models the effort docs list as supporting
// output_config.effort (platform.claude.com/docs/en/build-with-claude/effort).
// Others (e.g. claude-haiku-4-5) would reject the field, so it is omitted.
var anthropicEffortModels = map[string]bool{
	"claude-fable-5-2": true, "claude-mythos-5-2": true,
	"claude-fable-5-1": true, "claude-mythos-5-1": true,
	"claude-fable-5": true, "claude-mythos-5": true, "claude-mythos-preview": true,
	"claude-opus-5-5": true, "claude-opus-5": true,
	"claude-sonnet-5-5": true, "claude-sonnet-5": true,
	"claude-haiku-5-5": true,
	"claude-opus-4-8":  true, "claude-opus-4-7": true, "claude-opus-4-6": true,
	"claude-sonnet-4-6": true, "claude-opus-4-5-20251101": true,
}

func anthropicAcceptsEffort(model string) bool {
	if anthropicEffortModels[model] {
		return true
	}
	// Later Claude 5.x point releases (every 5.x model listed today supports
	// effort).
	for _, line := range []string{"opus", "sonnet", "haiku", "fable", "mythos"} {
		if strings.HasPrefix(model, "claude-"+line+"-5-") {
			return true
		}
	}
	return false
}

// AnthropicProvider implements LLMProvider for Anthropic Claude.
type AnthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider.
func NewAnthropicProvider(config *Config) *AnthropicProvider {
	model := config.AnthropicModel
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &AnthropicProvider{
		apiKey:  config.AnthropicAPIKey,
		model:   model,
		baseURL: anthropicBaseURL,
		client:  newCloudClient(60 * time.Second),
	}
}

func (p *AnthropicProvider) Name() string {
	return "Anthropic"
}

func (p *AnthropicProvider) IsConfigured() bool {
	return p.apiKey != ""
}

func (p *AnthropicProvider) DefaultModel() string { return p.model }

// Anthropic API types. No temperature field: removed in API version
// 2026-10-21, and Claude 4.7+ models reject non-default values on every
// version.
type anthropicRequest struct {
	Model        string                 `json:"model"`
	MaxTokens    int                    `json:"max_tokens"`
	Messages     []anthropicMessage     `json:"messages"`
	System       string                 `json:"system,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Role         string `json:"role"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence"`
	Content      []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *AnthropicProvider) setHeaders(r *http.Request) {
	r.Header.Set("x-api-key", p.apiKey)
	r.Header.Set("anthropic-version", anthropicAPIVersion)
}

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("Anthropic API key not configured")
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	// Extract system message and convert others
	var systemPrompt string
	var messages []anthropicMessage

	for _, m := range req.Messages {
		if m.Role == "system" {
			systemPrompt = m.Content
		} else {
			messages = append(messages, anthropicMessage(m))
		}
	}

	anthropicReq := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens + anthropicReasoningHeadroom,
		Messages:  messages,
		System:    systemPrompt,
	}
	if anthropicAcceptsEffort(model) {
		anthropicReq.OutputConfig = &anthropicOutputConfig{Effort: anthropicEffort}
	}

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()

	respBody, err := readBody(resp.Body, maxCompletionBody)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code, msg := decodeErrorFields(respBody)
		return nil, upstreamError(string(ProviderAnthropic), model, resp.StatusCode, code, msg, respBody)
	}

	var anthropicResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	// Combine all text content; thinking blocks are skipped.
	var content string
	for _, c := range anthropicResp.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	// A refusal can arrive with partial text; it is still not documentation.
	if anthropicResp.StopReason == "refusal" {
		return nil, &ResponseRefusedError{Provider: string(ProviderAnthropic), Model: model, Reason: "refusal", Detail: cleanDetail(content)}
	}
	if strings.TrimSpace(content) == "" {
		if anthropicResp.StopReason == "max_tokens" {
			return nil, fmt.Errorf("Anthropic model %s used its whole output budget without producing an answer; try again or pick a smaller model", model)
		}
		return nil, &ResponseRefusedError{Provider: string(ProviderAnthropic), Model: model, Reason: fmt.Sprintf("empty response, stop_reason %q", anthropicResp.StopReason)}
	}

	return &CompletionResponse{
		Content:      content,
		Model:        anthropicResp.Model,
		Provider:     "anthropic",
		TokensUsed:   anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		FinishReason: anthropicResp.StopReason,
	}, nil
}

// anthropicMaxPages bounds list pagination (1000 models per page).
const anthropicMaxPages = 5

// ListModels queries GET /v1/models (which by default lists active and
// deprecated models, newest first) and returns the active ones. Deprecated
// models are closed to new adopters, so they are not offered for selection.
func (p *AnthropicProvider) ListModels(ctx context.Context) ([]string, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("Anthropic API key not configured")
	}

	var ids []string
	afterID := ""
	for page := 0; page < anthropicMaxPages; page++ {
		q := url.Values{"limit": {"1000"}}
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models?"+q.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		p.setHeaders(httpReq)

		body, status, err := doList(p.client, httpReq)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			code, msg := decodeErrorFields(body)
			return nil, upstreamError(string(ProviderAnthropic), "", status, code, msg, body)
		}

		var list struct {
			Data []struct {
				ID        string `json:"id"`
				Lifecycle string `json:"lifecycle"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("parsing models response: %w", err)
		}
		for _, m := range list.Data {
			// lifecycle is absent on older API versions; treat as active.
			if m.Lifecycle == "" || m.Lifecycle == "active" {
				ids = append(ids, m.ID)
			}
		}
		if !list.HasMore || list.LastID == "" {
			break
		}
		afterID = list.LastID
	}
	return ids, nil
}
