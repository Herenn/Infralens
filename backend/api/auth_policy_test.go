package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Herenn/Infralens/backend/api/middleware"
)

// The v3.1 auth policy, end to end through NewServer().Handler() with
// environment-driven config, for the read paths the v3.1 panels use (Changes
// feed, Idle spend) next to the agent ingest paths:
//
//   - API_KEY only (the default production shape): the panels' reads are
//     public like every other topology read (the bundled UI sends no key);
//     ingest and /metrics need the key.
//   - UI_PASSWORD set: deny by default. The same reads need a session or the
//     API key; ingest and /metrics stay API-key-only (a session is not
//     enough).
//   - DRIFT_ENABLED=false: the feed is absent (404) in both shapes, once past
//     authentication.

var panelReadPaths = []string{
	"/api/v1/drift/events",
	"/api/v1/drift/summary",
	"/api/v1/graph/idle-cost",
}

const policyPassword = "correct horse battery staple"

func policyServer(t *testing.T, env map[string]string) *httptest.Server {
	t.Helper()
	base := map[string]string{
		"API_KEY": "", "ALLOW_NO_AUTH": "", "UI_PASSWORD": "", "SESSION_SECRET": "",
		"HISTORY_ENABLED": "true", "DRIFT_ENABLED": "", "MCP_API_KEY": "", "DEMO_MODE": "",
	}
	for k, v := range env {
		base[k] = v
	}
	srv := httptest.NewServer(newTestServer(t, base))
	t.Cleanup(srv.Close)
	return srv
}

func policyStatus(t *testing.T, srv *httptest.Server, method, path string, hdr map[string]string) int {
	t.Helper()
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func policyLogin(t *testing.T, srv *httptest.Server) map[string]string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"`+policyPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == middleware.SessionCookieName {
			return map[string]string{"Cookie": c.Name + "=" + c.Value, "Sec-Fetch-Site": "same-origin"}
		}
	}
	t.Fatalf("login: status %d, no session cookie", resp.StatusCode)
	return nil
}

func TestAuthPolicy_APIKeyOnly(t *testing.T) {
	srv := policyServer(t, map[string]string{"API_KEY": "agent-key"})
	key := map[string]string{"X-API-Key": "agent-key"}

	for _, p := range panelReadPaths {
		if got := policyStatus(t, srv, http.MethodGet, p, nil); got != http.StatusOK {
			t.Errorf("GET %s without a key: %d, want 200 (panels work without login)", p, got)
		}
	}
	for _, p := range []string{"/api/v1/events", "/api/v1/stats", "/api/v1/metrics", "/api/v1/inspection"} {
		if got := policyStatus(t, srv, http.MethodPost, p, nil); got != http.StatusUnauthorized {
			t.Errorf("POST %s without a key: %d, want 401", p, got)
		}
		if got := policyStatus(t, srv, http.MethodPost, p, key); got == http.StatusUnauthorized {
			t.Errorf("POST %s with the key: 401", p)
		}
	}
	if got := policyStatus(t, srv, http.MethodGet, "/metrics", nil); got != http.StatusUnauthorized {
		t.Errorf("GET /metrics without a key: %d, want 401", got)
	}
	// No login exists, so status says so and login is refused.
	if got := policyStatus(t, srv, http.MethodGet, "/api/v1/auth/status", nil); got != http.StatusOK {
		t.Errorf("GET /api/v1/auth/status: %d, want 200", got)
	}
}

func TestAuthPolicy_UIPassword(t *testing.T) {
	srv := policyServer(t, map[string]string{"API_KEY": "agent-key", "UI_PASSWORD": policyPassword})
	key := map[string]string{"X-API-Key": "agent-key"}
	session := policyLogin(t, srv)

	for _, p := range panelReadPaths {
		if got := policyStatus(t, srv, http.MethodGet, p, nil); got != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s: %d, want 401", p, got)
		}
		if got := policyStatus(t, srv, http.MethodGet, p, session); got != http.StatusOK {
			t.Errorf("GET %s with a session: %d, want 200", p, got)
		}
		if got := policyStatus(t, srv, http.MethodGet, p, key); got != http.StatusOK {
			t.Errorf("GET %s with the API key: %d, want 200", p, got)
		}
	}
	for _, p := range []string{"/api/v1/events", "/api/v1/stats"} {
		if got := policyStatus(t, srv, http.MethodPost, p, session); got != http.StatusUnauthorized {
			t.Errorf("POST %s with only a session: %d, want 401 (ingest is API-key-only)", p, got)
		}
		if got := policyStatus(t, srv, http.MethodPost, p, key); got == http.StatusUnauthorized {
			t.Errorf("POST %s with the key: 401", p)
		}
	}
	if got := policyStatus(t, srv, http.MethodGet, "/metrics", session); got != http.StatusUnauthorized {
		t.Errorf("GET /metrics with only a session: %d, want 401", got)
	}
	for _, p := range []string{"/health", "/ready", "/api/v1/auth/status"} {
		if got := policyStatus(t, srv, http.MethodGet, p, nil); got == http.StatusUnauthorized {
			t.Errorf("GET %s should stay public: 401", p)
		}
	}
}

func TestAuthPolicy_DriftDisabled(t *testing.T) {
	t.Run("no password", func(t *testing.T) {
		srv := policyServer(t, map[string]string{"API_KEY": "agent-key", "DRIFT_ENABLED": "false"})
		for _, p := range panelReadPaths[:2] {
			if got := policyStatus(t, srv, http.MethodGet, p, nil); got != http.StatusNotFound {
				t.Errorf("GET %s with DRIFT_ENABLED=false: %d, want 404", p, got)
			}
		}
	})
	t.Run("password", func(t *testing.T) {
		srv := policyServer(t, map[string]string{"API_KEY": "agent-key", "UI_PASSWORD": policyPassword, "DRIFT_ENABLED": "false"})
		session := policyLogin(t, srv)
		for _, p := range panelReadPaths[:2] {
			if got := policyStatus(t, srv, http.MethodGet, p, nil); got != http.StatusUnauthorized {
				t.Errorf("anonymous GET %s: %d, want 401 (auth is decided before routing)", p, got)
			}
			if got := policyStatus(t, srv, http.MethodGet, p, session); got != http.StatusNotFound {
				t.Errorf("GET %s with a session, DRIFT_ENABLED=false: %d, want 404", p, got)
			}
		}
		if got := policyStatus(t, srv, http.MethodGet, "/api/v1/graph/idle-cost", session); got != http.StatusOK {
			t.Errorf("idle spend is independent of DRIFT_ENABLED: %d, want 200", got)
		}
	})
}
