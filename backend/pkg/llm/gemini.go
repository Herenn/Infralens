package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiProvider implements LLMProvider for Google Gemini.
type GeminiProvider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(config *Config) *GeminiProvider {
	model := config.GeminiModel
	if model == "" {
		model = "gemini-pro"
	}
	return &GeminiProvider{
		apiKey: config.GeminiAPIKey,
		model:  model,
		client: &http.Client{
			Timeout: 60 * time.Second, // 60 second timeout for API calls
		},
	}
}

func (p *GeminiProvider) Name() string {
	return "Google Gemini"
}

func (p *GeminiProvider) IsConfigured() bool {
	return p.apiKey != ""
}

// Gemini API types
type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		SafetyRatings []struct {
			Category    string `json:"category"`
			Probability string `json:"probability"`
		} `json:"safetyRatings"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

func (p *GeminiProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 2048
	}

	temperature := req.Temperature
	if temperature == 0 {
		temperature = 0.7
	}

	// Convert messages to Gemini format
	// Gemini uses "user" and "model" roles
	var contents []geminiContent
	var systemPrompt string

	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			systemPrompt = m.Content
			continue
		}
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	// Prepend system prompt to first user message if present
	if systemPrompt != "" && len(contents) > 0 {
		contents[0].Parts[0].Text = systemPrompt + "\n\n" + contents[0].Parts[0].Text
	}

	geminiReq := geminiRequest{
		Contents: contents,
		GenerationConfig: &geminiGenerationConfig{
			MaxOutputTokens: maxTokens,
			Temperature:     temperature,
		},
	}

	body, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", geminiBaseURL, model, p.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", sanitizeTransportError(err))
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("Gemini error: %s", geminiResp.Error.Message)
	}

	if len(geminiResp.Candidates) == 0 {
		return nil, fmt.Errorf("no response from Gemini")
	}

	// Combine all text parts
	var content string
	for _, part := range geminiResp.Candidates[0].Content.Parts {
		content += part.Text
	}

	return &CompletionResponse{
		Content:      content,
		Model:        model,
		Provider:     "gemini",
		TokensUsed:   geminiResp.UsageMetadata.TotalTokenCount,
		FinishReason: geminiResp.Candidates[0].FinishReason,
	}, nil
}

func (p *GeminiProvider) ListModels(ctx context.Context) ([]string, error) {
	return []string{
		"gemini-pro",
		"gemini-pro-vision",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}, nil
}
