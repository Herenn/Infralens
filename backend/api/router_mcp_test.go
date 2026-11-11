package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/config"
	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

type mcpTestConfig struct {
	apiKey, mcpKey string
	uiPassword     string
	allowNoAuth    bool
	enabled        bool
	origins        []string
}

// newMCPTestServer builds the real server - router plus the full middleware
// chain - so the tests below exercise /mcp exactly as deployed.
func newMCPTestServer(t *testing.T, c mcpTestConfig) *httptest.Server {
	t.Helper()
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	def := middleware.DefaultAuthConfig()
	cors := middleware.DefaultCORSConfig()
	cors.AllowCredentials = false
	cfg := &config.Config{
		Server: config.ServerConfig{MaxRequestBytes: 10 << 20},
		CORS:   cors,
		Auth: middleware.AuthConfig{
			APIKey: c.apiKey, HeaderName: "X-API-Key",
			SkipPaths: def.SkipPaths, SkipPrefixes: def.SkipPrefixes,
			AllowNoAuth: c.allowNoAuth,
			UIPassword:  c.uiPassword,
		},
		MCP: config.MCPConfig{Enabled: c.enabled, APIKey: c.mcpKey, AllowedOrigins: c.origins},
	}
	s := NewServer(cfg, store, nil, llm.NewManager(nil))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close(); store.Close() })
	return srv
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

func do(t *testing.T, method, url, body string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data)
}

func bearer(k string) map[string]string { return map[string]string{"Authorization": "Bearer " + k} }

// mcpLogin logs in to the UI and returns the session cookie header value.
func mcpLogin(t *testing.T, base, password string) string {
	t.Helper()
	body := fmt.Sprintf(`{"username":"admin","password":%q}`, password)
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == middleware.SessionCookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatalf("login: status %d, no session cookie", resp.StatusCode)
	return ""
}

// TestMCPAuthMatrix pins the /mcp access rules for every combination of
// UI_PASSWORD x API_KEY x MCP_API_KEY x ALLOW_NO_AUTH:
//
//   - any key configured: a key is required (either configured key works);
//   - no key, UI_PASSWORD set: always refused, ALLOW_NO_AUTH or not, with a
//     message saying how to enable it - the UI's login must not be
//     bypassable through /mcp;
//   - no key, no UI_PASSWORD: open only with ALLOW_NO_AUTH.
//
// A UI session cookie never authenticates /mcp, and MCP_API_KEY is accepted
// on /mcp and nowhere else.
func TestMCPAuthMatrix(t *testing.T) {
	const password = "correct horse battery staple"
	for _, uiPassword := range []string{"", password} {
		for _, apiKey := range []string{"", "agent-key"} {
			for _, mcpKey := range []string{"", "reader-key"} {
				for _, allow := range []bool{false, true} {
					name := fmt.Sprintf("UI_PASSWORD=%v API_KEY=%q MCP_API_KEY=%q ALLOW_NO_AUTH=%v", uiPassword != "", apiKey, mcpKey, allow)
					t.Run(name, func(t *testing.T) {
						srv := newMCPTestServer(t, mcpTestConfig{apiKey: apiKey, mcpKey: mcpKey, uiPassword: uiPassword, allowNoAuth: allow, enabled: true})
						anyKey := apiKey != "" || mcpKey != ""
						open := !anyKey && allow && uiPassword == ""

						expect := func(label string, headers map[string]string, want int) string {
							t.Helper()
							status, _, body := do(t, http.MethodPost, srv.URL+"/mcp", initializeBody, headers)
							if status != want {
								t.Errorf("%s: /mcp status %d, want %d (%s)", label, status, want, body)
							}
							return body
						}
						if open {
							expect("no credentials (open)", nil, http.StatusOK)
						} else {
							body := expect("no credentials", nil, http.StatusUnauthorized)
							if !anyKey && !strings.Contains(body, "MCP_API_KEY") {
								t.Errorf("refusal without any key configured should say how to enable /mcp: %s", body)
							}
							if !anyKey && uiPassword != "" && !strings.Contains(body, "UI_PASSWORD") {
								t.Errorf("refusal under UI_PASSWORD should name the reason: %s", body)
							}
						}
						if apiKey != "" {
							expect("API_KEY as bearer", bearer(apiKey), http.StatusOK)
							expect("API_KEY as X-API-Key", map[string]string{"X-API-Key": apiKey}, http.StatusOK)
						}
						if mcpKey != "" {
							expect("MCP_API_KEY as bearer", bearer(mcpKey), http.StatusOK)
							expect("MCP_API_KEY as X-API-Key", map[string]string{"X-API-Key": mcpKey}, http.StatusOK)
						}
						if anyKey {
							expect("wrong key", bearer("nope"), http.StatusUnauthorized)
							expect("fake cookie only", map[string]string{"Cookie": "session=" + apiKey + mcpKey}, http.StatusUnauthorized)
						}
						if uiPassword != "" {
							// A real, valid UI session - accepted everywhere
							// else - is not a credential for /mcp.
							cookie := mcpLogin(t, srv.URL, uiPassword)
							if status, _, _ := do(t, http.MethodGet, srv.URL+"/api/v1/topology", "", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin"}); status != http.StatusOK {
								t.Fatalf("session cookie on /api/v1/topology: %d, want 200", status)
							}
							expect("valid session cookie only", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin"}, http.StatusUnauthorized)
						}

						// MCP_API_KEY must not unlock anything outside /mcp.
						if mcpKey != "" && apiKey != "" {
							status, _, _ := do(t, http.MethodPost, srv.URL+"/api/v1/events", `{}`, bearer(mcpKey))
							if status != http.StatusUnauthorized {
								t.Errorf("MCP_API_KEY on /api/v1/events: status %d, want 401", status)
							}
							status, _, _ = do(t, http.MethodGet, srv.URL+"/api/v1/ai/config", "", map[string]string{"X-API-Key": mcpKey})
							if status != http.StatusUnauthorized {
								t.Errorf("MCP_API_KEY on /api/v1/ai/config: status %d, want 401", status)
							}
							if status, _, _ := do(t, http.MethodPost, srv.URL+"/api/v1/events", `{}`, bearer(apiKey)); status == http.StatusUnauthorized {
								t.Error("API_KEY should still authenticate /api/v1/events")
							}
						}
						if mcpKey != "" && uiPassword != "" {
							if status, _, _ := do(t, http.MethodGet, srv.URL+"/api/v1/topology", "", bearer(mcpKey)); status != http.StatusUnauthorized {
								t.Errorf("MCP_API_KEY on /api/v1/topology behind UI login: status %d, want 401", status)
							}
						}
					})
				}
			}
		}
	}
}

func TestMCPPathHandling(t *testing.T) {
	srv := newMCPTestServer(t, mcpTestConfig{apiKey: "agent-key", mcpKey: "reader-key", enabled: true})

	// A trailing slash reaches the same endpoint, with the same auth.
	if status, _, _ := do(t, http.MethodPost, srv.URL+"/mcp/", initializeBody, bearer("reader-key")); status != http.StatusOK {
		t.Errorf("/mcp/: status %d, want 200", status)
	}
	if status, _, _ := do(t, http.MethodPost, srv.URL+"/mcp/", initializeBody, nil); status != http.StatusUnauthorized {
		t.Errorf("/mcp/ without key: status %d, want 401", status)
	}
	// Only the exact paths bypass the global chain; anything below /mcp/ and
	// dot-segment tricks go through it (and its API_KEY check) as before.
	for _, p := range []string{"/mcp/extra", "/mcpx", "/api/v1/services/../../mcp"} {
		status, _, _ := do(t, http.MethodPost, srv.URL+p, initializeBody, bearer("reader-key"))
		if status == http.StatusOK {
			t.Errorf("%s with MCP_API_KEY: status 200, want it handled outside /mcp", p)
		}
	}
}

// TestMCPCORSThroughRealChain proves a browser origin listed in
// MCP_ALLOWED_ORIGINS works end to end: the preflight is answered by the MCP
// endpoint (not the global CORS layer) with the MCP headers allowed, and the
// actual request is readable.
func TestMCPCORSThroughRealChain(t *testing.T) {
	const origin = "https://ops.example.com"
	srv := newMCPTestServer(t, mcpTestConfig{mcpKey: "reader-key", allowNoAuth: true, enabled: true, origins: []string{origin}})

	pre := map[string]string{
		"Origin":                         origin,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization,content-type,mcp-protocol-version,mcp-method,mcp-name",
	}
	status, h, _ := do(t, http.MethodOptions, srv.URL+"/mcp", "", pre)
	if status != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("preflight: %d ACAO=%q", status, h.Get("Access-Control-Allow-Origin"))
	}
	allowHeaders := strings.ToLower(h.Get("Access-Control-Allow-Headers"))
	for _, want := range []string{"mcp-protocol-version", "mcp-method", "mcp-name", "authorization"} {
		if !strings.Contains(allowHeaders, want) {
			t.Errorf("preflight Access-Control-Allow-Headers %q lacks %s", allowHeaders, want)
		}
	}

	headers := bearer("reader-key")
	headers["Origin"] = origin
	status, h, body := do(t, http.MethodPost, srv.URL+"/mcp", initializeBody, headers)
	if status != http.StatusOK || h.Get("Access-Control-Allow-Origin") != origin {
		t.Errorf("POST: %d ACAO=%q %s", status, h.Get("Access-Control-Allow-Origin"), body)
	}

	// One policy: an origin not in MCP_ALLOWED_ORIGINS is refused even though
	// the global CORS_ORIGINS is "*".
	pre["Origin"] = "https://evil.example"
	if status, h, _ := do(t, http.MethodOptions, srv.URL+"/mcp", "", pre); status != http.StatusForbidden || h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign preflight: %d ACAO=%q, want 403 and none", status, h.Get("Access-Control-Allow-Origin"))
	}

	// The rest of the API keeps its own CORS behaviour.
	pre["Origin"] = "https://anything.example"
	pre["Access-Control-Request-Method"] = "GET"
	delete(pre, "Access-Control-Request-Headers")
	if _, h, _ := do(t, http.MethodOptions, srv.URL+"/api/v1/topology", "", pre); h.Get("Access-Control-Allow-Origin") == "" {
		t.Error("global CORS no longer answers for /api/v1/topology")
	}
}

func TestMCPDisabled(t *testing.T) {
	srv := newMCPTestServer(t, mcpTestConfig{apiKey: "agent-key", mcpKey: "reader-key", enabled: false})
	if status, _, _ := do(t, http.MethodPost, srv.URL+"/mcp", initializeBody, bearer("agent-key")); status != http.StatusNotFound {
		t.Errorf("MCP_ENABLED=false: status %d, want 404", status)
	}
	// And the MCP key is then worthless everywhere.
	if status, _, _ := do(t, http.MethodPost, srv.URL+"/mcp", initializeBody, bearer("reader-key")); status != http.StatusUnauthorized {
		t.Errorf("MCP_ENABLED=false with MCP_API_KEY: status %d, want 401 from the global chain", status)
	}
}
