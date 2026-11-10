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

// OllamaProvider implements LLMProvider for local Ollama.
type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaProvider creates a new Ollama provider.
func NewOllamaProvider(config *Config) *OllamaProvider {
	baseURL := config.OllamaURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	// No hard-coded default model: when OLLAMA_MODEL is unset the first
	// locally installed model is used (see resolveModel).
	return &OllamaProvider{
		baseURL: baseURL,
		model:   config.OllamaModel,
		client: &http.Client{
			Timeout: 5 * time.Minute, // Local models can be slow
		},
	}
}

func (p *OllamaProvider) Name() string {
	return "Ollama"
}

// DefaultModel returns OLLAMA_MODEL, or "" when unset (first installed model
// is used at request time).
func (p *OllamaProvider) DefaultModel() string { return p.model }

// resolveModel picks the model for a request: explicit, configured, else the
// first installed model.
func (p *OllamaProvider) resolveModel(ctx context.Context, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if p.model != "" {
		return p.model, nil
	}
	models, err := p.ListModels(ctx)
	if err != nil {
		return "", fmt.Errorf("OLLAMA_MODEL is not set and installed models could not be listed: %w", err)
	}
	if len(models) == 0 {
		return "", fmt.Errorf("no Ollama models are installed; run `ollama pull %s` or set OLLAMA_MODEL", DefaultOllamaModel)
	}
	return models[0], nil
}

func (p *OllamaProvider) IsConfigured() bool {
	// Check if Ollama is running by pinging the API
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/api/tags", nil)
	if err != nil {
		return false
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// Ollama API types
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  *ollamaOptions  `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Message   struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
	Error              string `json:"error,omitempty"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
	} `json:"models"`
}

func (p *OllamaProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model, err := p.resolveModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	temperature := req.Temperature
	if temperature == 0 {
		temperature = 0.7
	}

	// Convert messages
	messages := make([]ollamaMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ollamaMessage{Role: m.Role, Content: m.Content}
	}

	ollamaReq := ollamaRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
		Options: &ollamaOptions{
			Temperature: temperature,
			NumPredict:  maxTokens,
		},
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, upstreamError(string(ProviderOllama), model, resp.StatusCode, code, msg, respBody)
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if ollamaResp.Error != "" {
		return nil, upstreamError(string(ProviderOllama), model, resp.StatusCode, "", ollamaResp.Error, nil)
	}
	if strings.TrimSpace(ollamaResp.Message.Content) == "" {
		return nil, &ResponseRefusedError{Provider: string(ProviderOllama), Model: model, Reason: fmt.Sprintf("empty response, done_reason %q", ollamaResp.DoneReason)}
	}

	return &CompletionResponse{
		Content:      ollamaResp.Message.Content,
		Model:        ollamaResp.Model,
		Provider:     "ollama",
		TokensUsed:   ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		FinishReason: ollamaResp.DoneReason,
	}, nil
}

func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	body, status, err := doList(p.client, req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		code, msg := decodeErrorFields(body)
		return nil, upstreamError(string(ProviderOllama), "", status, code, msg, body)
	}

	var tagsResp ollamaTagsResponse
	if err := json.Unmarshal(body, &tagsResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	// Installed models, minus embedding-only ones that cannot chat.
	var models []string
	for _, m := range tagsResp.Models {
		if m.Name != "" && isTextModel(m.Name) {
			models = append(models, m.Name)
		}
	}

	return models, nil
}

// LMStudioProvider implements LLMProvider for LM Studio (OpenAI-compatible).
type LMStudioProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewLMStudioProvider creates a new LM Studio provider.
func NewLMStudioProvider(config *Config) *LMStudioProvider {
	baseURL := config.LMStudioURL
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}
	return &LMStudioProvider{
		baseURL: baseURL,
		model:   config.LMStudioModel,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (p *LMStudioProvider) Name() string {
	return "LM Studio"
}

// DefaultModel returns LMSTUDIO_MODEL, or "" when unset (first loaded model
// is used at request time).
func (p *LMStudioProvider) DefaultModel() string { return p.model }

func (p *LMStudioProvider) IsConfigured() bool {
	// Check if LM Studio is running
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
	if err != nil {
		return false
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func (p *LMStudioProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	// LM Studio uses OpenAI-compatible API
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	if model == "" {
		// Best effort: name the first loaded model so errors can say which
		// model was used. If listing fails, send no model and let LM Studio
		// pick its loaded one, as before.
		if models, err := p.ListModels(ctx); err == nil && len(models) > 0 {
			model = models[0]
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	temperature := req.Temperature
	if temperature == 0 {
		temperature = 0.7
	}

	messages := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = openAIMessage{Role: m.Role, Content: m.Content}
	}

	openAIReq := openAIRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	body, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, upstreamError(string(ProviderLMStudio), model, resp.StatusCode, code, msg, respBody)
	}

	var openAIResp openAIResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if openAIResp.Error != nil {
		return nil, upstreamError(string(ProviderLMStudio), model, resp.StatusCode, openAIResp.Error.Code, openAIResp.Error.Message, nil)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LM Studio")
	}
	ch := openAIResp.Choices[0]
	if err := checkOpenAIChoice(string(ProviderLMStudio), model, ch.Message.Content, ch.Message.Refusal, ch.FinishReason); err != nil {
		return nil, err
	}

	return &CompletionResponse{
		Content:      openAIResp.Choices[0].Message.Content,
		Model:        openAIResp.Model,
		Provider:     "lmstudio",
		TokensUsed:   openAIResp.Usage.TotalTokens,
		FinishReason: openAIResp.Choices[0].FinishReason,
	}, nil
}

func (p *LMStudioProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	body, status, err := doList(p.client, req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		code, msg := decodeErrorFields(body)
		return nil, upstreamError(string(ProviderLMStudio), "", status, code, msg, body)
	}

	var modelsResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	var models []string
	for _, m := range modelsResp.Data {
		if m.ID != "" && isTextModel(m.ID) {
			models = append(models, m.ID)
		}
	}

	return models, nil
}
