package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okHandler records that the request got through and whether its context
// carried a deadline (i.e. was bound to a session).
type okHandler struct {
	reached     bool
	hasDeadline bool
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.reached = true
	_, h.hasDeadline = r.Context().Deadline()
	w.WriteHeader(http.StatusOK)
}

// Every route a UI or tool reads, plus routes that do not exist yet: with a
// password set all of them must be closed to anonymous callers.
var protectedWhenPasswordSet = []struct{ method, path string }{
	{"GET", "/api/v1/topology"},
	{"GET", "/api/v1/topology?at=2026-11-01T00:00:00Z"},
	{"GET", "/api/v1/topology/export"},
	{"GET", "/api/v1/topology/history/range"},
	{"GET", "/api/v1/topology/history/stale"},
	{"GET", "/api/v1/topology/history/diff"},
	{"GET", "/api/v1/services"},
	{"GET", "/api/v1/services/10.0.0.1%2Fnginx"},
	{"GET", "/api/v1/services/10.0.0.1/nginx"},
	{"GET", "/api/v1/graph/stats"},
	{"GET", "/api/v1/graph/criticality"},
	{"GET", "/api/v1/graph/orphans"},
	{"GET", "/api/v1/graph/impact?service=x"},
	{"GET", "/api/v1/k8s/status"},
	{"GET", "/api/v1/version"},
	{"GET", "/api/v1/ws"},
	{"GET", "/api/v1/ai/status"},
	{"GET", "/api/v1/ai/providers"},
	{"GET", "/api/v1/ai/config"},
	{"POST", "/api/v1/ai/config"},
	{"POST", "/api/v1/ai/docs"},
	{"POST", "/api/v1/ai/ask"},
	// Not routed today - deny-by-default must cover them anyway.
	{"GET", "/api/v1/drift/events"},
	{"GET", "/api/v1/graph/idle-cost"},
	{"POST", "/mcp"},
	{"GET", "/api/v2/anything"},
	{"GET", "/"},
}

func TestUIAuth_PasswordSet_RejectsAnonymous(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())

	for _, tc := range protectedWhenPasswordSet {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h := &okHandler{}
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(h).ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != http.StatusUnauthorized || h.reached {
				t.Errorf("anonymous %s %s: got %d (reached=%v), want 401", tc.method, tc.path, rr.Code, h.reached)
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("401 Content-Type = %q, want application/json", ct)
			}
		})
	}
}

func TestUIAuth_PasswordSet_AcceptsSession(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())
	cookie := loginCookie(t, sm)

	for _, tc := range protectedWhenPasswordSet {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			h := &okHandler{}
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.AddCookie(cookie)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(h).ServeHTTP(rr, r)
			if rr.Code != http.StatusOK || !h.reached {
				t.Errorf("session %s %s: got %d, want 200", tc.method, tc.path, rr.Code)
			}
			if !h.hasDeadline {
				t.Error("session-authenticated request context is not bound to the session lifetime")
			}
		})
	}
}

func TestUIAuth_PasswordSet_AcceptsAPIKey(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())

	for name, set := range map[string]func(*http.Request){
		"X-API-Key": func(r *http.Request) { r.Header.Set("X-API-Key", cfg.APIKey) },
		"Bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+cfg.APIKey) },
	} {
		for _, tc := range protectedWhenPasswordSet {
			t.Run(name+" "+tc.method+" "+tc.path, func(t *testing.T) {
				h := &okHandler{}
				r := httptest.NewRequest(tc.method, tc.path, nil)
				set(r)
				// API-key callers are not browsers: no origin check applies.
				r.Header.Set("Origin", "https://elsewhere.example")
				rr := httptest.NewRecorder()
				UIAuth(cfg, sm)(h).ServeHTTP(rr, r)
				if rr.Code != http.StatusOK {
					t.Errorf("%s %s with %s: got %d, want 200", tc.method, tc.path, name, rr.Code)
				}
			})
		}
	}
}

func TestUIAuth_PasswordSet_RejectsBadCredentials(t *testing.T) {
	cfg := testAuthConfig()
	clock := newFakeClock()
	sm := newTestSessions(t, cfg, clock)
	valid := loginCookie(t, sm)

	other := newTestSessions(t, cfg, clock) // different random key
	foreign := loginCookie(t, other)

	cases := map[string]func(*http.Request){
		"wrong API key":                func(r *http.Request) { r.Header.Set("X-API-Key", "nope") },
		"wrong bearer":                 func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		"basic auth is not a key":      func(r *http.Request) { r.SetBasicAuth("admin", cfg.UIPassword) },
		"password as API key":          func(r *http.Request) { r.Header.Set("X-API-Key", cfg.UIPassword) },
		"forged cookie":                func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "v1.a.9999999999.b"}) },
		"cookie from another instance": func(r *http.Request) { r.AddCookie(foreign) },
		"wrong API key beats a valid cookie": func(r *http.Request) {
			r.AddCookie(valid)
			r.Header.Set("X-API-Key", "nope")
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			h := &okHandler{}
			r := httptest.NewRequest("GET", "/api/v1/topology", nil)
			set(r)
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(h).ServeHTTP(rr, r)
			if rr.Code != http.StatusUnauthorized || h.reached {
				t.Errorf("%s: got %d, want 401", name, rr.Code)
			}
		})
	}
}

func TestUIAuth_PasswordSet_ExpiredSessionRejected(t *testing.T) {
	cfg := testAuthConfig()
	cfg.SessionTTL = time.Hour
	clock := newFakeClock()
	sm := newTestSessions(t, cfg, clock)
	cookie := loginCookie(t, sm)
	clock.Advance(time.Hour + time.Second)

	r := httptest.NewRequest("GET", "/api/v1/topology", nil)
	r.AddCookie(cookie)
	rr := httptest.NewRecorder()
	UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expired session: got %d, want 401", rr.Code)
	}
}

func TestUIAuth_PasswordSet_PublicPaths(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())

	for _, p := range []string{"/health", "/ready", "/api/v1/auth/status", "/api/v1/auth/login", "/api/v1/auth/logout"} {
		t.Run(p, func(t *testing.T) {
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
			if rr.Code != http.StatusOK {
				t.Errorf("%s: got %d, want 200", p, rr.Code)
			}
		})
	}
}

// Agent ingestion and Prometheus keep v3.0 semantics: the API key and only the
// API key, whether or not a UI password is set.
func TestUIAuth_PasswordSet_AgentEndpointsAreAPIKeyOnly(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())
	cookie := loginCookie(t, sm)

	for _, p := range []string{"/api/v1/events", "/api/v1/stats", "/api/v1/metrics", "/api/v1/inspection", "/metrics"} {
		t.Run(p, func(t *testing.T) {
			run := func(set func(*http.Request)) int {
				r := httptest.NewRequest("POST", p, nil)
				set(r)
				rr := httptest.NewRecorder()
				UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
				return rr.Code
			}
			if got := run(func(r *http.Request) { r.Header.Set("X-API-Key", cfg.APIKey) }); got != http.StatusOK {
				t.Errorf("with API key: got %d, want 200", got)
			}
			if got := run(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+cfg.APIKey) }); got != http.StatusOK {
				t.Errorf("with Bearer API key: got %d, want 200", got)
			}
			if got := run(func(r *http.Request) {}); got != http.StatusUnauthorized {
				t.Errorf("anonymous: got %d, want 401", got)
			}
			if got := run(func(r *http.Request) {
				r.AddCookie(cookie)
				r.Header.Set("Sec-Fetch-Site", "same-origin")
			}); got != http.StatusUnauthorized {
				t.Errorf("session cookie only: got %d, want 401", got)
			}
		})
	}

	// ALLOW_NO_AUTH with a UI password: agent endpoints stay open as in v3.0,
	// reads are still closed.
	open := cfg
	open.APIKey = ""
	open.AllowNoAuth = true
	smOpen := newTestSessions(t, open, newFakeClock())
	rr := httptest.NewRecorder()
	UIAuth(open, smOpen)(&okHandler{}).ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/events", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("ALLOW_NO_AUTH ingest: got %d, want 200", rr.Code)
	}
	rr = httptest.NewRecorder()
	UIAuth(open, smOpen)(&okHandler{}).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/topology", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("ALLOW_NO_AUTH read with UI password: got %d, want 401", rr.Code)
	}
}

// Exemptions are exact matches on an already-clean path. Anything dirty is
// treated as protected regardless of what it cleans to, so the auth decision
// can never be made on a different path than the one a router serves.
func TestUIAuth_PasswordSet_PathTricksDoNotBypass(t *testing.T) {
	cfg := testAuthConfig()
	cfg.APIKey = "" // so the agent paths would be "open" if reached by a trick
	cfg.AllowNoAuth = true
	sm := newTestSessions(t, cfg, newFakeClock())

	for _, p := range []string{
		"/health/../api/v1/topology",
		"/api/v1/auth/status/../../v1/topology",
		"/api/v1/auth/login/..",
		"/api/v1/services/x/../../../health",
		"/api/v1/services/x/../../auth/status",
		"/api/v1/events/../topology",
		"/api/v1/events/../../v1/ws",
		"//api/v1/auth/status",
		"/api/v1//auth/status",
		"/api/v1/auth/status/",
		"/api/v1/auth/./status",
		"/health/",
		"/metrics/../api/v1/topology",
		"/api/v1/metrics/./../topology",
	} {
		t.Run(p, func(t *testing.T) {
			r := httptest.NewRequest("GET", p, nil)
			if r.URL.Path != p {
				t.Fatalf("test setup invalid: %q was cleaned to %q before UIAuth saw it", p, r.URL.Path)
			}
			h := &okHandler{}
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(h).ServeHTTP(rr, r)
			if rr.Code != http.StatusUnauthorized || h.reached {
				t.Errorf("dirty path %q: got %d (reached=%v), want 401", p, rr.Code, h.reached)
			}
		})
	}

	// Percent-encoding decodes into URL.Path before UIAuth runs, so an encoded
	// traversal is just another dirty path.
	r := httptest.NewRequest("GET", "/health%2F..%2Fapi%2Fv1%2Ftopology", nil)
	rr := httptest.NewRecorder()
	UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("encoded traversal %q: got %d, want 401", r.URL.Path, rr.Code)
	}
}

// CSRF and cross-site WebSocket hijacking: a cookie alone is not enough for a
// state-changing request or a WebSocket from a foreign origin.
func TestUIAuth_PasswordSet_CrossOriginCookieRequests(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())
	cookie := loginCookie(t, sm)

	cases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    int
	}{
		{"cross-site POST", "POST", "/api/v1/ai/config",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"sibling subdomain POST", "POST", "/api/v1/ai/ask",
			map[string]string{"Origin": "https://evil.infralens.example", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"old browser cross-origin POST", "POST", "/api/v1/ai/config",
			map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"same-origin POST", "POST", "/api/v1/ai/config",
			map[string]string{"Origin": "http://infralens.example", "Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"trusted CORS origin POST", "POST", "/api/v1/ai/config",
			map[string]string{"Origin": "https://trusted.example", "Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		{"cross-site WebSocket", "GET", "/api/v1/ws",
			map[string]string{"Origin": "https://evil.example", "Upgrade": "websocket", "Connection": "Upgrade"}, http.StatusForbidden},
		{"same-origin WebSocket", "GET", "/api/v1/ws",
			map[string]string{"Origin": "http://infralens.example", "Upgrade": "websocket", "Connection": "Upgrade"}, http.StatusOK},
		// Reads too: with CORS credentials allowed, a foreign page could
		// otherwise read the response.
		{"cross-site GET", "GET", "/api/v1/topology",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Host = "infralens.example"
			r.AddCookie(cookie)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// With UI_PASSWORD unset, behaviour is v3.0's exactly, plus /api/v1/auth/*
// being reachable so the UI can find out no login is needed.
func TestUIAuth_PasswordUnset_MatchesV30(t *testing.T) {
	cfg := DefaultAuthConfig()
	cfg.APIKey = "secret"

	tests := []struct {
		path     string
		wantCode int
	}{
		{"/health", http.StatusOK},
		{"/ready", http.StatusOK},
		{"/api/v1/ws", http.StatusOK},
		{"/api/v1/topology", http.StatusOK},
		{"/api/v1/topology/export", http.StatusOK},
		{"/api/v1/topology/history/range", http.StatusOK},
		{"/api/v1/topology/history/stale", http.StatusOK},
		{"/api/v1/topology/history/diff", http.StatusOK},
		{"/api/v1/services", http.StatusOK},
		{"/api/v1/services/10.0.0.1%2Fnginx", http.StatusOK},
		{"/api/v1/graph/stats", http.StatusOK},
		{"/api/v1/graph/criticality", http.StatusOK},
		{"/api/v1/graph/orphans", http.StatusOK},
		{"/api/v1/graph/impact?service=x", http.StatusOK},
		{"/api/v1/k8s/status", http.StatusOK},
		{"/api/v1/ai/status", http.StatusOK},
		{"/api/v1/ai/providers", http.StatusOK},
		{"/api/v1/auth/status", http.StatusOK},
		{"/api/v1/auth/login", http.StatusOK},
		{"/api/v1/auth/logout", http.StatusOK},

		{"/api/v1/events", http.StatusUnauthorized},
		{"/api/v1/inspection", http.StatusUnauthorized},
		{"/api/v1/ai/config", http.StatusUnauthorized},
		{"/api/v1/ai/ask", http.StatusUnauthorized},
		{"/api/v1/version", http.StatusUnauthorized},
		{"/api/v1/topology-admin", http.StatusUnauthorized},
		{"/api/v1/services-admin", http.StatusUnauthorized},
		{"/api/v1/auth/status-admin", http.StatusUnauthorized},
		{"/api/v1/auth/status/../../v1/events", http.StatusUnauthorized},
		{"/metrics", http.StatusUnauthorized},
	}

	legacy := Auth(cfg)
	ui := UIAuth(cfg, nil)
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			for _, method := range []string{"GET", "POST"} {
				rr := httptest.NewRecorder()
				ui(&okHandler{}).ServeHTTP(rr, httptest.NewRequest(method, tc.path, nil))
				if rr.Code != tc.wantCode {
					t.Errorf("%s %s: got %d, want %d", method, tc.path, rr.Code, tc.wantCode)
				}

				// Anything not under /api/v1/auth/ must match v3.0's Auth exactly.
				if len(tc.path) < len("/api/v1/auth/") || tc.path[:len("/api/v1/auth/")] != "/api/v1/auth/" {
					lr := httptest.NewRecorder()
					legacy(&okHandler{}).ServeHTTP(lr, httptest.NewRequest(method, tc.path, nil))
					if lr.Code != rr.Code {
						t.Errorf("%s %s: UIAuth gave %d but v3.0 Auth gives %d", method, tc.path, rr.Code, lr.Code)
					}
				}
			}
		})
	}

	// And with no API key at all (ALLOW_NO_AUTH), everything is open, as before.
	open := DefaultAuthConfig()
	open.AllowNoAuth = true
	for _, p := range []string{"/api/v1/topology", "/api/v1/events", "/api/v1/ai/config", "/api/v1/brand-new"} {
		rr := httptest.NewRecorder()
		UIAuth(open, nil)(&okHandler{}).ServeHTTP(rr, httptest.NewRequest("POST", p, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("no API key, no password: %s got %d, want 200", p, rr.Code)
		}
	}
}
