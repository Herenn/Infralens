package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const openAIBaseURL = "https://api.openai.com/v1"

// Reasoning headroom is added to the caller's MaxTokens for cloud models that
// reason before answering. On OpenAI, Anthropic and Gemini alike, reasoning
// tokens count against the output cap, so a cap sized for the visible answer
// alone can be spent entirely on thinking and return an empty reply. Only
// tokens actually generated are billed, so headroom raises the ceiling, not
// the cost of a normal request; the effort settings are what keep the
// typical cost down.
//
// OpenAI's reasoning guide recommends "reserving at least 25,000 tokens for
// reasoning and outputs" (developers.openai.com/api/docs/guides/reasoning).
const openAIReasoningHeadroom = 25000

// openAIReasoningEffort is sent as reasoning_effort to models known to accept
// it. "low" is accepted by every current GPT-6 model (6 Luna and 6.1 Terra:
// none..max; 6.1 Sol and 6 Astra: low..max, where "none" would be a 400), and
// by GPT-5.x and o-series reasoning models. A service summary does not
// benefit from the default "medium".
const openAIReasoningEffort = "low"

// openAIAcceptsReasoningEffort reports whether model is a reasoning model that
// takes reasoning_effort on Chat Completions. Non-reasoning models (gpt-4o,
// gpt-4.1, *-chat-latest) reject the parameter, so it is only sent when the
// model family is known to accept it.
func openAIAcceptsReasoningEffort(model string) bool {
	m := strings.ToLower(model)
	if strings.Contains(m, "chat") {
		return false
	}
	if strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "gpt-6") {
		return true
	}
	return len(m) > 1 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9'
}

// checkOpenAIChoice turns refusals, content-filter stops and empty answers
// from an OpenAI-compatible response into errors instead of empty "docs".
func checkOpenAIChoice(provider, model, content, refusal, finish string) error {
	switch {
	case refusal != "":
		return &ResponseRefusedError{Provider: provider, Model: model, Reason: "refusal", Detail: cleanDetail(refusal)}
	case finish == "content_filter":
		return &ResponseRefusedError{Provider: provider, Model: model, Reason: "content_filter"}
	case strings.TrimSpace(content) == "" && finish == "length":
		return fmt.Errorf("%s model %s used its whole output budget without producing an answer; try again or pick a model with less reasoning", providerDisplayName(provider), model)
	case strings.TrimSpace(content) == "":
		return &ResponseRefusedError{Provider: provider, Model: model, Reason: fmt.Sprintf("empty response, finish_reason %q", finish)}
	}
	return nil
}

// OpenAIProvider implements LLMProvider for OpenAI.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(config *Config) *OpenAIProvider {
	model := config.OpenAIModel
	if model == "" {
		model = DefaultOpenAIModel
	}
	return &OpenAIProvider{
		apiKey:  config.OpenAIAPIKey,
		model:   model,
		baseURL: openAIBaseURL,
		client:  newCloudClient(60 * time.Second),
	}
}

func (p *OpenAIProvider) Name() string {
	return "OpenAI"
}

func (p *OpenAIProvider) IsConfigured() bool {
	return p.apiKey != ""
}

func (p *OpenAIProvider) DefaultModel() string { return p.model }

// OpenAI API types. Shared with LM Studio's OpenAI-compatible endpoint, which
// is why both max_tokens and max_completion_tokens exist: OpenAI deprecated
// max_tokens (it is rejected by reasoning models) in favour of
// max_completion_tokens, while local OpenAI-compatible servers still expect
// max_tokens.
type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	Temperature         float64         `json:"temperature,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("OpenAI API key not configured")
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	// Convert messages. OpenAI still accepts the "system" role on Chat
	// Completions (newer models treat it as "developer").
	messages := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = openAIMessage{Role: m.Role, Content: m.Content}
	}

	// temperature is deliberately not sent: current reasoning models (GPT-5.x,
	// GPT-6) reject any non-default value with HTTP 400.
	openAIReq := openAIRequest{
		Model:               model,
		Messages:            messages,
		MaxCompletionTokens: maxTokens + openAIReasoningHeadroom,
	}
	if openAIAcceptsReasoningEffort(model) {
		openAIReq.ReasoningEffort = openAIReasoningEffort
	}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

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
		return nil, upstreamError(string(ProviderOpenAI), model, resp.StatusCode, code, msg, respBody)
	}

	var openAIResp openAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	choice := openAIResp.Choices[0]
	if err := checkOpenAIChoice(string(ProviderOpenAI), model, choice.Message.Content, choice.Message.Refusal, choice.FinishReason); err != nil {
		return nil, err
	}

	return &CompletionResponse{
		Content:      choice.Message.Content,
		Model:        openAIResp.Model,
		Provider:     "openai",
		TokensUsed:   openAIResp.Usage.TotalTokens,
		FinishReason: choice.FinishReason,
	}, nil
}

// ListModels queries GET /v1/models and keeps chat-capable models, newest
// first.
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("OpenAI API key not configured")
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	body, status, err := doList(p.client, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		code, msg := decodeErrorFields(body)
		return nil, upstreamError(string(ProviderOpenAI), "", status, code, msg, body)
	}

	var list struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parsing models response: %w", err)
	}

	created := make(map[string]int64, len(list.Data))
	var ids []string
	for _, m := range list.Data {
		if openAIChatModel(m.ID) {
			ids = append(ids, m.ID)
			created[m.ID] = m.Created
		}
	}
	sortNewestFirst(ids, created)
	return ids, nil
}

// doList performs a list-models GET with the transport hardening applied:
// URL stripped from transport errors, body size capped.
func doList(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching models: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body, maxListBody)
	if err != nil {
		return nil, 0, fmt.Errorf("reading models response: %w", sanitizeTransportError(err))
	}
	return body, resp.StatusCode, nil
}
