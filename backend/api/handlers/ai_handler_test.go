package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/pkg/llm"
)

// writeBody writes a canned response body from an httptest handler. It runs on
// the server goroutine, so it reports with t.Errorf rather than t.Fatalf.
func writeBody(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing test response: %v", err)
	}
}

// fakeOllama mimics Ollama: one installed model, and /api/chat answering 404
// "model not found" for anything else, as Ollama does for a model that was
// never pulled.
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			writeBody(t, w, `{"models":[{"name":"gemma4:e4b"},{"name":"qwen3.5:4b"}]}`)
		case "/api/chat":
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding chat request: %v", err)
			}
			if body.Model != "gemma4:e4b" {
				w.WriteHeader(http.StatusNotFound)
				writeBody(t, w, `{"error":"model \"`+body.Model+`\" not found, try pulling it first"}`)
				return
			}
			writeBody(t, w, `{"model":"gemma4:e4b","message":{"role":"assistant","content":"# Docs"},"done":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedService ingests one connection and returns a service ID.
func seedService(t *testing.T, ts *testServer) string {
	t.Helper()
	batch := map[string]interface{}{
		"node_name": "test-node",
		"timestamp": time.Now().Format(time.RFC3339),
		"events": []map[string]interface{}{
			{"pid": 1234, "comm": "nginx", "src_addr": "10.0.0.1", "dst_addr": "10.0.0.2", "dst_port": 80, "direction": 0},
		},
	}
	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ts.router.ServeHTTP(httptest.NewRecorder(), req)

	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/topology", nil))
	var topo struct {
		Services []struct {
			ID string `json:"id"`
		} `json:"services"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&topo); err != nil || len(topo.Services) == 0 {
		t.Fatalf("no service seeded: %v", err)
	}
	return topo.Services[0].ID
}

func newAIHandler(t *testing.T, ts *testServer, ollamaModel string) *handlers.AIHandler {
	t.Helper()
	srv := fakeOllama(t)
	mgr := llm.NewManager(&llm.Config{
		OllamaURL:       srv.URL,
		OllamaModel:     ollamaModel,
		LMStudioURL:     "http://127.0.0.1:1", // nothing listens: unconfigured
		DefaultProvider: llm.ProviderOllama,
	})
	return handlers.NewAIHandler(ts.topology, mgr)
}

func TestHandleDocs_RejectedModelIsExplained(t *testing.T) {
	ts := newTestServer(t)
	id := seedService(t, ts)
	h := newAIHandler(t, ts, "llama2")

	rr := httptest.NewRecorder()
	h.HandleDocs(rr, httptest.NewRequest("POST", "/api/v1/ai/docs?serviceId="+url.QueryEscape(id), nil))

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Error       string   `json:"error"`
		Code        string   `json:"code"`
		Provider    string   `json:"provider"`
		Model       string   `json:"model"`
		Suggestions []string `json:"suggestions"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != "model_unavailable" || resp.Provider != "ollama" || resp.Model != "llama2" {
		t.Errorf("unexpected body %+v", resp)
	}
	if len(resp.Suggestions) == 0 || resp.Suggestions[0] != "gemma4:e4b" {
		t.Errorf("suggestions should come from the live installed list, got %v", resp.Suggestions)
	}
}

func TestHandleDocs_EmptyAnswerIsAnErrorNotDocs(t *testing.T) {
	ts := newTestServer(t)
	id := seedService(t, ts)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			writeBody(t, w, `{"models":[{"name":"gemma4:e4b"}]}`)
			return
		}
		writeBody(t, w, `{"model":"gemma4:e4b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(srv.Close)
	h := handlers.NewAIHandler(ts.topology, llm.NewManager(&llm.Config{
		OllamaURL: srv.URL, LMStudioURL: "http://127.0.0.1:1", DefaultProvider: llm.ProviderOllama,
	}))

	rr := httptest.NewRecorder()
	h.HandleDocs(rr, httptest.NewRequest("POST", "/api/v1/ai/docs?serviceId="+url.QueryEscape(id), nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Code != "response_refused" {
		t.Errorf("code = %q", resp.Code)
	}
}

func TestHandleDocs_SucceedsWithFirstInstalledModel(t *testing.T) {
	ts := newTestServer(t)
	id := seedService(t, ts)
	h := newAIHandler(t, ts, "") // no OLLAMA_MODEL: first installed is used

	rr := httptest.NewRecorder()
	h.HandleDocs(rr, httptest.NewRequest("POST", "/api/v1/ai/docs?serviceId="+url.QueryEscape(id), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rr.Code, rr.Body.String())
	}
	var resp llm.DocumentationResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Model != "gemma4:e4b" || resp.Content == "" {
		t.Errorf("unexpected %+v", resp)
	}
}

func TestHandleProviders_ListsLiveAndFallbackModels(t *testing.T) {
	ts := newTestServer(t)
	h := newAIHandler(t, ts, "")

	rr := httptest.NewRecorder()
	h.HandleProviders(rr, httptest.NewRequest("GET", "/api/v1/ai/providers", nil))
	var resp struct {
		Providers []struct {
			ID           string   `json:"id"`
			Models       []string `json:"models"`
			DefaultModel string   `json:"default_model"`
			Source       string   `json:"models_source"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, p := range resp.Providers {
		byID[p.ID] = i
	}
	if len(resp.Providers) != 5 {
		t.Fatalf("want 5 providers, got %d", len(resp.Providers))
	}
	ol := resp.Providers[byID["ollama"]]
	if ol.Source != "live" || ol.DefaultModel != "gemma4:e4b" || len(ol.Models) != 2 {
		t.Errorf("ollama: %+v", ol)
	}
	an := resp.Providers[byID["anthropic"]]
	if an.Source != "fallback" || an.DefaultModel != llm.DefaultAnthropicModel || len(an.Models) == 0 {
		t.Errorf("anthropic: %+v", an)
	}
	lm := resp.Providers[byID["lmstudio"]]
	if lm.Models == nil {
		t.Error("models must be [] rather than null for the UI")
	}
}

func TestHandleConfig_EmptyFieldsKeepExistingValues(t *testing.T) {
	ts := newTestServer(t)
	mgr := llm.NewManager(&llm.Config{
		OpenAIAPIKey:    "sk-from-env",
		OpenAIModel:     "gpt-6.1-sol",
		OllamaURL:       "http://127.0.0.1:1",
		LMStudioURL:     "http://127.0.0.1:1",
		DefaultProvider: llm.ProviderOpenAI,
	})
	h := handlers.NewAIHandler(ts.topology, mgr)

	body := `{"anthropic_api_key":"sk-ant-new","anthropic_model":"claude-sonnet-5-5"}`
	rr := httptest.NewRecorder()
	h.HandleConfig(rr, httptest.NewRequest("POST", "/api/v1/ai/config", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}

	cfg := mgr.Config()
	if cfg.OpenAIAPIKey != "sk-from-env" || cfg.OpenAIModel != "gpt-6.1-sol" || cfg.DefaultProvider != llm.ProviderOpenAI {
		t.Errorf("existing settings were wiped: %+v", cfg)
	}
	if cfg.AnthropicAPIKey != "sk-ant-new" || cfg.AnthropicModel != "claude-sonnet-5-5" {
		t.Errorf("new settings not applied: %+v", cfg)
	}

	// GET exposes models and default provider, never keys.
	rr = httptest.NewRecorder()
	h.HandleConfig(rr, httptest.NewRequest("GET", "/api/v1/ai/config", nil))
	if bytes.Contains(rr.Body.Bytes(), []byte("sk-from-env")) || bytes.Contains(rr.Body.Bytes(), []byte("sk-ant-new")) {
		t.Fatalf("GET /ai/config leaked an API key: %s", rr.Body.String())
	}
	var got struct {
		DefaultProvider string            `json:"default_provider"`
		Models          map[string]string `json:"models"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.DefaultProvider != "openai" || got.Models["anthropic"] != "claude-sonnet-5-5" {
		t.Errorf("unexpected GET body %+v", got)
	}
}

func TestHandleConfig_ClearRemovesKeysAndResetsModels(t *testing.T) {
	ts := newTestServer(t)
	mgr := llm.NewManager(&llm.Config{
		OpenAIAPIKey:    "sk-leaked",
		OpenAIModel:     "gpt-6-luna-typo",
		AnthropicAPIKey: "sk-ant-keep",
		OllamaURL:       "http://127.0.0.1:1",
		LMStudioURL:     "http://127.0.0.1:1",
		DefaultProvider: llm.ProviderOpenAI,
	})
	h := handlers.NewAIHandler(ts.topology, mgr)

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.HandleConfig(rr, httptest.NewRequest("POST", "/api/v1/ai/config", bytes.NewBufferString(body)))
		return rr
	}

	rr := post(`{"clear":["openai_api_key","openai_model","default_provider"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	cfg := mgr.Config()
	if cfg.OpenAIAPIKey != "" || cfg.OpenAIModel != "" || cfg.DefaultProvider != "" {
		t.Errorf("clear did not apply: %+v", cfg)
	}
	if cfg.AnthropicAPIKey != "sk-ant-keep" {
		t.Errorf("an unrelated key was removed: %+v", cfg)
	}
	if p, _ := mgr.GetProvider(llm.ProviderOpenAI); p.IsConfigured() || p.DefaultModel() != llm.DefaultOpenAIModel {
		t.Errorf("OpenAI provider should be unconfigured with the default model, got configured=%v model=%q", p.IsConfigured(), p.DefaultModel())
	}

	if rr := post(`{"clear":["openai_apikey"]}`); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown clear field: status %d, want 400", rr.Code)
	}
	if rr := post(`{"gemini_model":"gemini-3.8-flash","clear":["gemini_model"]}`); rr.Code != http.StatusBadRequest {
		t.Errorf("set-and-clear: status %d, want 400", rr.Code)
	}
}
