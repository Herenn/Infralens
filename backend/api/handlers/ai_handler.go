package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// AIHandler handles AI documentation endpoints.
type AIHandler struct {
	topology   *service.TopologyService
	llmManager *llm.Manager
	docsGen    *llm.DocsGenerator
}

// NewAIHandler creates a new AI handler.
func NewAIHandler(topology *service.TopologyService, llmManager *llm.Manager) *AIHandler {
	h := &AIHandler{
		topology:   topology,
		llmManager: llmManager,
	}
	if llmManager != nil {
		h.docsGen = llm.NewDocsGenerator(llmManager)
	}
	return h
}

// HandleStatus returns the status of AI providers.
func (h *AIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.llmManager == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":   false,
			"providers": map[string]bool{},
			"message":   "AI documentation not configured",
		})
		return
	}

	status := h.llmManager.Status()
	hasConfigured := false
	for _, configured := range status {
		if configured {
			hasConfigured = true
			break
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":   hasConfigured,
		"providers": status,
	})
}

// AIConfigRequest represents a request to update AI configuration.
type AIConfigRequest struct {
	OpenAIAPIKey    string `json:"openai_api_key,omitempty"`
	OpenAIModel     string `json:"openai_model,omitempty"`
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string `json:"anthropic_model,omitempty"`
	GeminiAPIKey    string `json:"gemini_api_key,omitempty"`
	GeminiModel     string `json:"gemini_model,omitempty"`
	OllamaURL       string `json:"ollama_url,omitempty"`
	OllamaModel     string `json:"ollama_model,omitempty"`
	LMStudioURL     string `json:"lmstudio_url,omitempty"`
	LMStudioModel   string `json:"lmstudio_model,omitempty"`
	DefaultProvider string `json:"default_provider,omitempty"`
}

// HandleConfig gets or sets AI configuration.
func (h *AIHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		if h.llmManager == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"configured": false,
			})
			return
		}

		status := h.llmManager.Status()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": true,
			"providers":  status,
		})
		return
	}

	// POST: Update configuration
	var req AIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	config := &llm.Config{
		OpenAIAPIKey:    req.OpenAIAPIKey,
		OpenAIModel:     req.OpenAIModel,
		AnthropicAPIKey: req.AnthropicAPIKey,
		AnthropicModel:  req.AnthropicModel,
		GeminiAPIKey:    req.GeminiAPIKey,
		GeminiModel:     req.GeminiModel,
		OllamaURL:       req.OllamaURL,
		OllamaModel:     req.OllamaModel,
		LMStudioURL:     req.LMStudioURL,
		LMStudioModel:   req.LMStudioModel,
		DefaultProvider: llm.Provider(req.DefaultProvider),
	}

	if h.llmManager == nil {
		h.llmManager = llm.NewManager(config)
		h.docsGen = llm.NewDocsGenerator(h.llmManager)
	} else {
		h.llmManager.UpdateConfig(config)
	}

	log.Info("AI configuration updated")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "updated",
		"providers": h.llmManager.Status(),
	})
}

// AIDocsRequest represents a request for AI documentation.
type AIDocsRequest struct {
	Provider string `json:"provider,omitempty"`
}

// HandleDocs generates AI documentation for a service.
func (h *AIHandler) HandleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.docsGen == nil {
		http.Error(w, "AI documentation not configured. Please set API keys first.", http.StatusServiceUnavailable)
		return
	}

	serviceID := r.URL.Query().Get("serviceId")
	if serviceID == "" {
		http.Error(w, "serviceId query parameter is required", http.StatusBadRequest)
		return
	}

	var req AIDocsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	svc, err := h.topology.GetService(ctx, serviceID)
	if err != nil {
		http.Error(w, "Failed to get service", http.StatusInternalServerError)
		return
	}
	if svc == nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	// Build service context for LLM
	llmCtx := h.buildServiceContext(ctx, svc)

	docReq := llm.DocumentationRequest{
		Context: llmCtx,
		Format:  "markdown",
	}

	var resp *llm.DocumentationResponse
	if req.Provider != "" {
		resp, err = h.docsGen.GenerateWithProvider(ctx, llm.Provider(req.Provider), docReq)
	} else {
		resp, err = h.docsGen.GenerateDocumentation(ctx, docReq)
	}

	if err != nil {
		log.WithError(err).Error("Failed to generate AI documentation")
		http.Error(w, fmt.Sprintf("AI generation failed: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(resp)
}

// AIAskRequest represents a question about a service.
type AIAskRequest struct {
	Question string `json:"question"`
	Provider string `json:"provider,omitempty"`
}

// HandleAsk answers a specific question about a service.
func (h *AIHandler) HandleAsk(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h.docsGen == nil {
		http.Error(w, "AI documentation not configured. Please set API keys first.", http.StatusServiceUnavailable)
		return
	}

	serviceID := r.URL.Query().Get("serviceId")
	if serviceID == "" {
		http.Error(w, "serviceId query parameter is required", http.StatusBadRequest)
		return
	}

	var req AIAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	svc, err := h.topology.GetService(ctx, serviceID)
	if err != nil {
		http.Error(w, "Failed to get service", http.StatusInternalServerError)
		return
	}
	if svc == nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	llmCtx := h.buildServiceContext(ctx, svc)

	docReq := llm.DocumentationRequest{
		Context:  llmCtx,
		Question: req.Question,
	}

	resp, err := h.docsGen.AskQuestion(ctx, docReq)
	if err != nil {
		log.WithError(err).Error("Failed to answer question")
		http.Error(w, fmt.Sprintf("AI query failed: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(resp)
}

// HandleProviders returns available AI providers.
func (h *AIHandler) HandleProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	providers := []map[string]interface{}{
		{
			"id":          "openai",
			"name":        "OpenAI",
			"description": "GPT-4 and GPT-3.5 models",
			"models":      []string{"gpt-4-turbo-preview", "gpt-4", "gpt-3.5-turbo"},
			"requires":    "api_key",
		},
		{
			"id":          "anthropic",
			"name":        "Anthropic",
			"description": "Claude 3 models",
			"models":      []string{"claude-3-opus-20240229", "claude-3-sonnet-20240229", "claude-3-haiku-20240307"},
			"requires":    "api_key",
		},
		{
			"id":          "gemini",
			"name":        "Google Gemini",
			"description": "Gemini Pro models",
			"models":      []string{"gemini-pro", "gemini-1.5-pro", "gemini-1.5-flash"},
			"requires":    "api_key",
		},
		{
			"id":          "ollama",
			"name":        "Ollama (Local)",
			"description": "Local LLM via Ollama",
			"models":      []string{"llama2", "mistral", "codellama"},
			"requires":    "local_server",
		},
		{
			"id":          "lmstudio",
			"name":        "LM Studio (Local)",
			"description": "Local LLM via LM Studio",
			"models":      []string{},
			"requires":    "local_server",
		},
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
	})
}

// buildServiceContext builds an LLM service context from storage service.
func (h *AIHandler) buildServiceContext(ctx context.Context, svc *storage.Service) llm.ServiceContext {
	llmCtx := llm.ServiceContext{
		ServiceID:   svc.ID,
		ServiceName: svc.Name,
		ServiceType: svc.Type,
		Technology:  svc.Tech,
		NodeName:    svc.Node,
		IPAddress:   svc.PodIP,
	}

	// Get connections for this service
	topology, err := h.topology.GetTopology(ctx)
	if err == nil {
		for _, conn := range topology.Connections {
			connInfo := llm.ConnectionInfo{
				Port:        int(conn.Port),
				BytesPerSec: conn.BytesSentRate + conn.BytesRecvRate,
			}

			if conn.SourceID == svc.ID {
				// Find target service
				for _, targetSvc := range topology.Services {
					if targetSvc.ID == conn.TargetID {
						connInfo.RemoteIP = targetSvc.PodIP
						connInfo.RemoteName = targetSvc.Name
						break
					}
				}
				llmCtx.OutgoingConnections = append(llmCtx.OutgoingConnections, connInfo)
			} else if conn.TargetID == svc.ID {
				// Find source service
				for _, sourceSvc := range topology.Services {
					if sourceSvc.ID == conn.SourceID {
						connInfo.RemoteIP = sourceSvc.PodIP
						connInfo.RemoteName = sourceSvc.Name
						break
					}
				}
				llmCtx.IncomingConnections = append(llmCtx.IncomingConnections, connInfo)
			}
		}
	}

	// TODO: Add inspection data from storage
	// This would require fetching inspection data and deserializing JSON fields

	return llmCtx
}
