package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// AIHandler handles AI documentation endpoints.
type AIHandler struct {
	topology *service.TopologyService

	// mu guards llmManager/docsGen, which HandleConfig replaces while other
	// requests are reading them.
	mu         sync.RWMutex
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

// deps returns the current manager and docs generator under a read lock.
func (h *AIHandler) deps() (*llm.Manager, *llm.DocsGenerator) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.llmManager, h.docsGen
}

// HandleStatus returns the status of AI providers.
func (h *AIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	llmManager, _ := h.deps()
	if llmManager == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":   false,
			"providers": map[string]bool{},
			"message":   "AI documentation not configured",
		})
		return
	}

	status := llmManager.Status()
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
//
// Deliberately no ollama_url / lmstudio_url fields. Those name hosts the
// backend then issues requests to, so accepting them over the API turns this
// endpoint into a server-side request forgery primitive (cloud metadata
// endpoints, internal services) with the response surfaced back to the caller.
// Local-LLM endpoints are configuration, not user input: set OLLAMA_URL and
// LMSTUDIO_URL in the environment instead.
type AIConfigRequest struct {
	OpenAIAPIKey    string `json:"openai_api_key,omitempty"`
	OpenAIModel     string `json:"openai_model,omitempty"`
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string `json:"anthropic_model,omitempty"`
	GeminiAPIKey    string `json:"gemini_api_key,omitempty"`
	GeminiModel     string `json:"gemini_model,omitempty"`
	OllamaModel     string `json:"ollama_model,omitempty"`
	LMStudioModel   string `json:"lmstudio_model,omitempty"`
	DefaultProvider string `json:"default_provider,omitempty"`

	// Clear lists fields to reset: an API key is removed, a model or the
	// default provider goes back to the built-in default. Omitted or empty
	// fields are left unchanged, so this is the only way to remove a value.
	// Values configured from the environment are cleared for this process
	// only; they come back on restart.
	Clear []string `json:"clear,omitempty"`
}

// clearableAIFields maps the names accepted in AIConfigRequest.Clear to the
// config field they reset.
func clearableAIFields(c *llm.Config) map[string]*string {
	return map[string]*string{
		"openai_api_key":    &c.OpenAIAPIKey,
		"openai_model":      &c.OpenAIModel,
		"anthropic_api_key": &c.AnthropicAPIKey,
		"anthropic_model":   &c.AnthropicModel,
		"gemini_api_key":    &c.GeminiAPIKey,
		"gemini_model":      &c.GeminiModel,
		"ollama_model":      &c.OllamaModel,
		"lmstudio_model":    &c.LMStudioModel,
	}
}

// requestAsConfig copies the submitted values into a fresh llm.Config.
func requestAsConfig(req AIConfigRequest) *llm.Config {
	return &llm.Config{
		OpenAIAPIKey:    req.OpenAIAPIKey,
		OpenAIModel:     req.OpenAIModel,
		AnthropicAPIKey: req.AnthropicAPIKey,
		AnthropicModel:  req.AnthropicModel,
		GeminiAPIKey:    req.GeminiAPIKey,
		GeminiModel:     req.GeminiModel,
		OllamaModel:     req.OllamaModel,
		LMStudioModel:   req.LMStudioModel,
		DefaultProvider: llm.Provider(req.DefaultProvider),
	}
}

// HandleConfig gets or sets AI configuration.
func (h *AIHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	llmManager, _ := h.deps()

	if r.Method == "GET" {
		if llmManager == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"configured": false,
			})
			return
		}

		cfg := llmManager.Config()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":       true,
			"providers":        llmManager.Status(),
			"default_provider": string(cfg.DefaultProvider),
			// Configured model per provider (empty = provider default). Never
			// includes API keys.
			"models": map[string]string{
				"openai":    cfg.OpenAIModel,
				"anthropic": cfg.AnthropicModel,
				"gemini":    cfg.GeminiModel,
				"ollama":    cfg.OllamaModel,
				"lmstudio":  cfg.LMStudioModel,
			},
		})
		return
	}

	// POST: Update configuration
	var req AIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	config := requestAsConfig(req)

	// Local-LLM endpoints stay whatever the environment configured them to be;
	// they are never taken from the request body.
	if llmManager != nil {
		if existing := llmManager.Config(); existing != nil {
			config.OllamaURL = existing.OllamaURL
			config.LMStudioURL = existing.LMStudioURL
			// An omitted/empty field keeps the current value. Without this, a
			// UI save that only sets one provider's key would silently erase
			// the keys and *_MODEL choices configured from the environment.
			keepIfEmpty(&config.OpenAIAPIKey, existing.OpenAIAPIKey)
			keepIfEmpty(&config.OpenAIModel, existing.OpenAIModel)
			keepIfEmpty(&config.AnthropicAPIKey, existing.AnthropicAPIKey)
			keepIfEmpty(&config.AnthropicModel, existing.AnthropicModel)
			keepIfEmpty(&config.GeminiAPIKey, existing.GeminiAPIKey)
			keepIfEmpty(&config.GeminiModel, existing.GeminiModel)
			keepIfEmpty(&config.OllamaModel, existing.OllamaModel)
			keepIfEmpty(&config.LMStudioModel, existing.LMStudioModel)
			if config.DefaultProvider == "" {
				config.DefaultProvider = existing.DefaultProvider
			}
		}
	}

	// Explicit clears. Setting and clearing the same field in one request is
	// ambiguous, so it is rejected rather than guessed at.
	fields := clearableAIFields(config)
	submitted := clearableAIFields(requestAsConfig(req))
	for _, name := range req.Clear {
		if name == "default_provider" {
			if req.DefaultProvider != "" {
				http.Error(w, "default_provider is both set and cleared", http.StatusBadRequest)
				return
			}
			config.DefaultProvider = ""
			continue
		}
		field, ok := fields[name]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown field in clear: %q", name), http.StatusBadRequest)
			return
		}
		if *submitted[name] != "" {
			http.Error(w, fmt.Sprintf("%s is both set and cleared", name), http.StatusBadRequest)
			return
		}
		*field = ""
	}

	h.mu.Lock()
	if h.llmManager == nil {
		h.llmManager = llm.NewManager(config)
		h.docsGen = llm.NewDocsGenerator(h.llmManager)
	} else {
		h.llmManager.UpdateConfig(config)
	}
	manager := h.llmManager
	h.mu.Unlock()

	log.Info("AI configuration updated")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "updated",
		"providers": manager.Status(),
	})
}

// AIDocsRequest represents a request for AI documentation.
type AIDocsRequest struct {
	Provider string `json:"provider,omitempty"`
}

// HandleDocs generates AI documentation for a service.
func (h *AIHandler) HandleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	_, docsGen := h.deps()
	if docsGen == nil {
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
		resp, err = docsGen.GenerateWithProvider(ctx, llm.Provider(req.Provider), docReq)
	} else {
		resp, err = docsGen.GenerateDocumentation(ctx, docReq)
	}

	if err != nil {
		log.WithError(err).Error("Failed to generate AI documentation")
		if h.writeModelError(w, r, err) {
			return
		}
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

	_, docsGen := h.deps()
	if docsGen == nil {
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

	resp, err := docsGen.AskQuestion(ctx, docReq)
	if err != nil {
		log.WithError(err).Error("Failed to answer question")
		if h.writeModelError(w, r, err) {
			return
		}
		http.Error(w, fmt.Sprintf("AI query failed: %v", err), http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(resp)
}

// HandleProviders returns available AI providers and their models.
//
// Model lists come from each provider's list-models API when the provider is
// configured (cached in the llm package, with a timeout), and fall back to a
// short static list otherwise; "models_source" says which. The response is a
// superset of the v3.0 shape (id/name/description/models/requires).
func (h *AIHandler) HandleProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	meta := []struct {
		id, name, description, requires string
	}{
		{"openai", "OpenAI", "OpenAI GPT models", "api_key"},
		{"anthropic", "Anthropic", "Anthropic Claude models", "api_key"},
		{"gemini", "Google Gemini", "Google Gemini models", "api_key"},
		{"ollama", "Ollama (Local)", "Local LLM via Ollama", "local_server"},
		{"lmstudio", "LM Studio (Local)", "Local LLM via LM Studio", "local_server"},
	}

	llmManager, _ := h.deps()
	catalogs := map[string]llm.ModelCatalog{}
	if llmManager != nil {
		for _, c := range llmManager.AllModels(r.Context()) {
			catalogs[c.Provider] = c
		}
	}

	providers := make([]map[string]interface{}, 0, len(meta))
	for _, m := range meta {
		c, ok := catalogs[m.id]
		if !ok {
			c = llm.ModelCatalog{Provider: m.id, Models: llm.FallbackModels(llm.Provider(m.id)), Source: llm.ModelSourceFallback}
		}
		models := c.Models
		if models == nil {
			models = []string{}
		}
		entry := map[string]interface{}{
			"id":            m.id,
			"name":          m.name,
			"description":   m.description,
			"models":        models,
			"default_model": c.DefaultModel,
			"models_source": c.Source,
			"configured":    c.Configured,
			"requires":      m.requires,
		}
		if c.Error != "" {
			entry["models_error"] = c.Error
		}
		providers = append(providers, entry)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": providers,
	})
}

// writeModelError answers with HTTP 422 and a structured JSON body when err
// means the provider rejected the requested model, so the UI can say so
// plainly and offer alternatives. Returns false, writing nothing, for any
// other error.
//
// A refused/blocked/empty answer (llm.ResponseRefusedError) is reported as
// HTTP 502 with code "response_refused" and the provider's reason.
func (h *AIHandler) writeModelError(w http.ResponseWriter, r *http.Request, err error) bool {
	var re *llm.ResponseRefusedError
	if errors.As(err, &re) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":    re.Error(),
			"code":     "response_refused",
			"provider": re.Provider,
			"model":    re.Model,
			"reason":   re.Reason,
		})
		return true
	}
	var me *llm.ModelError
	if !errors.As(err, &me) {
		return false
	}
	suggestions := []string{}
	if llmManager, _ := h.deps(); llmManager != nil {
		if s := llmManager.SuggestModels(r.Context(), llm.Provider(me.Provider), me.Model, 5); s != nil {
			suggestions = s
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":       me.Error(),
		"code":        "model_unavailable",
		"provider":    me.Provider,
		"model":       me.Model,
		"detail":      me.Detail,
		"suggestions": suggestions,
	})
	return true
}

func keepIfEmpty(field *string, existing string) {
	if *field == "" {
		*field = existing
	}
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
						// The DNS name this service itself used beats a ":443" -
						// for address-keyed targets only (the ones with derived
						// names), like every other label; a Kubernetes-resolved
						// target keeps its Kubernetes name.
						if names := storage.HostnameNames(conn.Hostnames); len(names) > 0 && len(targetSvc.Hostnames) > 0 {
							connInfo.RemoteName = names[0]
						}
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
