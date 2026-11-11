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

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiProvider implements LLMProvider for Google Gemini.
//
// It uses the generateContent endpoint, which Google documents as "fully
// supported" alongside the newer Interactions API. The API key is sent in
// the x-goog-api-key header (Google's documented form) rather than the ?key=
// query parameter, so it can never appear in a logged request URL.
type GeminiProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewGeminiProvider creates a new Gemini provider.
func NewGeminiProvider(config *Config) *GeminiProvider {
	model := config.GeminiModel
	if model == "" {
		model = DefaultGeminiModel
	}
	return &GeminiProvider{
		apiKey:  config.GeminiAPIKey,
		model:   model,
		baseURL: geminiBaseURL,
		client:  newCloudClient(60 * time.Second),
	}
}

func (p *GeminiProvider) Name() string {
	return "Google Gemini"
}

func (p *GeminiProvider) IsConfigured() bool {
	return p.apiKey != ""
}

func (p *GeminiProvider) DefaultModel() string { return p.model }

// Gemini API types
type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// No temperature: Google's docs say custom sampling values are silently
// ignored on Gemini 3.6 Flash and newer, so the API default is used for all
// models.
type geminiGenerationConfig struct {
	MaxOutputTokens int                   `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiThinkingConfig struct {
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// geminiReasoningHeadroom is added to maxOutputTokens: Gemini 3.x thinks by
// default and the output cap "includ[es] thought tokens".
const geminiReasoningHeadroom = 16000

// geminiThinkingLevel picks thinkingConfig.thinkingLevel for a model, or ""
// to leave the model default. Per Google's thinking docs, Gemini 3.6 Flash
// and 3.8 Flash default to "medium" and accept "low"; Flash-Lite models
// already default to "minimal" (3.5 Flash-Lite), so sending "low" there would
// increase thinking, and 2.5 models use the older thinkingBudget knob. Only
// non-Lite Gemini 3.x therefore gets "low".
func geminiThinkingLevel(model string) string {
	m := strings.ToLower(strings.TrimPrefix(model, "models/"))
	if strings.HasPrefix(m, "gemini-3") && !strings.Contains(m, "lite") {
		return "low"
	}
	return ""
}

// geminiBlockedFinish are finishReason values meaning the output was
// withheld by a filter rather than completed.
var geminiBlockedFinish = map[string]bool{
	"SAFETY": true, "RECITATION": true, "BLOCKLIST": true, "PROHIBITED_CONTENT": true,
	"SPII": true, "IMAGE_SAFETY": true, "LANGUAGE": true, "OTHER": true,
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought,omitempty"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason        string `json:"blockReason"`
		BlockReasonMessage string `json:"blockReasonMessage"`
	} `json:"promptFeedback"`
	ModelVersion  string `json:"modelVersion"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// geminiModelPath turns a configured model name into the escaped path
// segment used in the request URL. Accepts "gemini-x" or "models/gemini-x".
// Escaping keeps a model name taken from the config API from altering the
// request path or query.
func geminiModelPath(model string) string {
	return url.PathEscape(strings.TrimPrefix(model, "models/"))
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

	// Convert messages to Gemini format: "user" and "model" roles, system
	// prompt in systemInstruction.
	var contents []geminiContent
	var system *geminiContent

	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			system = &geminiContent{Parts: []geminiPart{{Text: m.Content}}}
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

	geminiReq := geminiRequest{
		Contents:          contents,
		SystemInstruction: system,
		GenerationConfig: &geminiGenerationConfig{
			MaxOutputTokens: maxTokens + geminiReasoningHeadroom,
		},
	}
	if level := geminiThinkingLevel(model); level != "" {
		geminiReq.GenerationConfig.ThinkingConfig = &geminiThinkingConfig{ThinkingLevel: level}
	}

	body, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, geminiModelPath(model))
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

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
		return nil, upstreamError(string(ProviderGemini), model, resp.StatusCode, code, msg, respBody)
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	// A blocked prompt comes back as 200 with promptFeedback and no candidates.
	if br := geminiResp.PromptFeedback.BlockReason; br != "" {
		return nil, &ResponseRefusedError{Provider: string(ProviderGemini), Model: model, Reason: "prompt blocked: " + br, Detail: cleanDetail(geminiResp.PromptFeedback.BlockReasonMessage)}
	}
	if len(geminiResp.Candidates) == 0 {
		return nil, &ResponseRefusedError{Provider: string(ProviderGemini), Model: model, Reason: "no candidates returned"}
	}

	// Combine all text parts, skipping thought summaries.
	var content string
	for _, part := range geminiResp.Candidates[0].Content.Parts {
		if !part.Thought {
			content += part.Text
		}
	}
	finish := geminiResp.Candidates[0].FinishReason
	if geminiBlockedFinish[finish] {
		return nil, &ResponseRefusedError{Provider: string(ProviderGemini), Model: model, Reason: "output withheld: " + finish}
	}
	if strings.TrimSpace(content) == "" {
		if finish == "MAX_TOKENS" {
			return nil, fmt.Errorf("Gemini model %s used its whole output budget without producing an answer; try again or pick another model", model)
		}
		return nil, &ResponseRefusedError{Provider: string(ProviderGemini), Model: model, Reason: fmt.Sprintf("empty response, finishReason %q", finish)}
	}

	respModel := geminiResp.ModelVersion
	if respModel == "" {
		respModel = model
	}

	return &CompletionResponse{
		Content:      content,
		Model:        respModel,
		Provider:     "gemini",
		TokensUsed:   geminiResp.UsageMetadata.TotalTokenCount,
		FinishReason: finish,
	}, nil
}

// geminiMaxPages bounds list pagination.
const geminiMaxPages = 5

// ListModels queries GET /v1beta/models and keeps text models that support
// generateContent.
func (p *GeminiProvider) ListModels(ctx context.Context) ([]string, error) {
	if !p.IsConfigured() {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	var ids []string
	pageToken := ""
	for page := 0; page < geminiMaxPages; page++ {
		q := url.Values{"pageSize": {"1000"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models?"+q.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		httpReq.Header.Set("x-goog-api-key", p.apiKey)

		body, status, err := doList(p.client, httpReq)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			code, msg := decodeErrorFields(body)
			return nil, upstreamError(string(ProviderGemini), "", status, code, msg, body)
		}

		var list struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("parsing models response: %w", err)
		}
		for _, m := range list.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if !strings.HasPrefix(id, "gemini-") || !isTextModel(id) {
				continue
			}
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					ids = append(ids, id)
					break
				}
			}
		}
		if list.NextPageToken == "" {
			break
		}
		pageToken = list.NextPageToken
	}
	return ids, nil
}
