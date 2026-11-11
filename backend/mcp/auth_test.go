package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrossOriginPolicy(t *testing.T) {
	_, srv := newTestEndpoint(t, Options{AllowedOrigins: []string{"https://infralens.example.com"}})

	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,authorization,mcp-protocol-version,mcp-method,mcp-name")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	for _, origin := range []string{"https://infralens.example.com", "http://localhost:6274"} {
		resp := preflight(origin)
		if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("preflight from %s: %d ACAO=%q", origin, resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
		}
		ach := strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers"))
		for _, h := range []string{"authorization", "mcp-protocol-version", "mcp-method", "mcp-name", "content-type"} {
			if !strings.Contains(ach, h) {
				t.Errorf("preflight from %s: Access-Control-Allow-Headers %q lacks %s", origin, ach, h)
			}
		}
		if resp.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Error("credentials mode must never be enabled: /mcp does not use cookies")
		}
	}
	if resp := preflight("https://evil.example"); resp.StatusCode != http.StatusForbidden || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight from a foreign origin: %d ACAO=%q, want 403 and no ACAO", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}

	// The actual request from an allowed origin is readable by the page.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://infralens.example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "https://infralens.example.com" {
		t.Errorf("POST from allowed origin: %d ACAO=%q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestKeyAuthenticator(t *testing.T) {
	req := func(headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name                   string
		auth                   Authenticator
		headers                map[string]string
		allowed, authenticated bool
	}{
		{"no keys, open", KeyAuthenticator(nil, true), nil, true, false},
		{"no keys, open, key sent anyway", KeyAuthenticator([]string{""}, true), map[string]string{"X-API-Key": "x"}, true, false},
		{"no keys, closed", KeyAuthenticator(nil, false), nil, false, false},
		{"key configured, none sent, open flag ignored", KeyAuthenticator([]string{"k1"}, true), nil, false, false},
		{"bearer, second key", KeyAuthenticator([]string{"k1", "k2"}, false), map[string]string{"Authorization": "Bearer k2"}, true, true},
		{"lower-case bearer scheme", KeyAuthenticator([]string{"k1"}, false), map[string]string{"Authorization": "bearer k1"}, true, true},
		{"X-API-Key", KeyAuthenticator([]string{"k1"}, false), map[string]string{"X-API-Key": "k1"}, true, true},
		{"custom header", KeyAuthenticator([]string{"k1"}, false, "X-Token"), map[string]string{"X-Token": "k1"}, true, true},
		{"wrong key", KeyAuthenticator([]string{"k1"}, true), map[string]string{"Authorization": "Bearer k"}, false, false},
		{"key with suffix", KeyAuthenticator([]string{"k1"}, true), map[string]string{"X-API-Key": "k1extra"}, false, false},
		{"cookie never authenticates", KeyAuthenticator([]string{"k1"}, false), map[string]string{"Cookie": "session=k1; api_key=k1"}, false, false},
		{"basic scheme ignored", KeyAuthenticator([]string{"k1"}, false), map[string]string{"Authorization": "Basic k1"}, false, false},
	}
	for _, tc := range cases {
		allowed, authed := tc.auth(req(tc.headers))
		if allowed != tc.allowed || authed != tc.authenticated {
			t.Errorf("%s: allowed=%v authenticated=%v, want %v/%v", tc.name, allowed, authed, tc.allowed, tc.authenticated)
		}
	}
}

func TestHandlerEnforcesAuth(t *testing.T) {
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`

	// A nil Authenticator fails closed.
	closed := httptest.NewServer(NewHandler(Deps{}, Options{}))
	t.Cleanup(closed.Close)
	if r := (&rpcClient{t: t, url: closed.URL}).post(initBody, nil); r.status != http.StatusUnauthorized {
		t.Errorf("nil Auth: status %d, want 401", r.status)
	}

	_, srv := newTestEndpoint(t, Options{Auth: KeyAuthenticator([]string{"read-only"}, false)})
	c := &rpcClient{t: t, url: srv.URL}
	if r := c.post(initBody, nil); r.status != http.StatusUnauthorized || rpcCode(r) != codeInvalidRequest {
		t.Errorf("no key: %d %s", r.status, r.raw)
	}
	if r := c.post(initBody, map[string]string{"Authorization": "Bearer read-only"}); r.status != 200 {
		t.Errorf("valid key: %d %s", r.status, r.raw)
	}

	// Preflights carry no credentials and must not be refused for that.
	req, _ := http.NewRequest(http.MethodOptions, srv.URL, nil)
	req.Header.Set("Origin", "http://localhost:6274")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("unauthenticated preflight: %d, want 204", resp.StatusCode)
	}
}

// The process fingerprint is not served by any public REST endpoint, so it
// is only returned to key-authenticated callers.
func TestFingerprintOnlyForAuthenticatedCallers(t *testing.T) {
	call := func(srvURL string, headers map[string]string) map[string]any {
		h := map[string]string{headerProtocolVersion: "2025-11-25"}
		for k, v := range headers {
			h[k] = v
		}
		r := (&rpcClient{t: t, url: srvURL}).post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_service","arguments":{"service":"payments-db"}}}`, h)
		return r.body["result"].(map[string]any)["structuredContent"].(map[string]any)
	}
	_, open := newTestEndpoint(t, Options{Auth: KeyAuthenticator(nil, true)})
	if _, ok := call(open.URL, nil)["process_fingerprint"]; ok {
		t.Error("open mode must not return the process fingerprint")
	}
	_, keyed := newTestEndpoint(t, Options{Auth: KeyAuthenticator([]string{"k"}, false)})
	if _, ok := call(keyed.URL, map[string]string{"X-API-Key": "k"})["process_fingerprint"]; !ok {
		t.Error("authenticated callers should get the process fingerprint")
	}
}
