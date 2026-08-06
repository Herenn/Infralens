package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AuthConfig
		wantErr bool
	}{
		{"key set", AuthConfig{APIKey: "secret"}, false},
		{"no key, not opted out", AuthConfig{}, true},
		{"no key, explicitly opted out", AuthConfig{AllowNoAuth: true}, false},
		{"key set and opted out", AuthConfig{APIKey: "secret", AllowNoAuth: true}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// TestAuthSkipsOnlyDeclaredPaths guards the skip list against the prefix
// matching it used to do, where any path merely starting with a public one —
// including routes added later — was exempted from authentication.
// TestAuthResistsDotSegmentTraversal is a regression test. net/url.Parse does
// not collapse "." / ".." path segments, so a request for
// "/api/v1/services/../events" arrives at the handler with that literal,
// uncleaned path. Naive prefix matching against cfg.SkipPrefixes would see it
// start with the exempted "/api/v1/services/" prefix and let it straight
// through to a protected route with zero authentication.
//
// gorilla/mux happens to intercept any dirty path on its own, before route
// matching, and respond with a bare redirect instead of running a handler -
// which is what actually prevented this from being exploitable even before
// this fix (verified separately, live, against a real binary: the redirect
// carries no side effects, and following it to the clean path enforces auth
// normally). But that protection is mux's default, not something this
// middleware controls, and it would silently stop applying the moment
// anything called router.SkipClean(true) for an unrelated reason elsewhere in
// the codebase. This test pins down that Auth is correct on its own, by
// exercising it directly with a dirty path and no router involved at all.
func TestAuthResistsDotSegmentTraversal(t *testing.T) {
	cfg := DefaultAuthConfig()
	cfg.APIKey = "secret"

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Each of these cleans (via path.Clean, the same normalization a
	// well-behaved router applies) to a protected route, despite the raw text
	// starting with the exempted /api/v1/services/ prefix.
	dirtyPaths := []string{
		"/api/v1/services/../events",
		"/api/v1/services/../../v1/ai/config",
		"/api/v1/services//../events",
		"/api/v1/services/./../events",
	}

	for _, p := range dirtyPaths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest("POST", p, nil)
			// Confirm the test setup actually reflects what a real request
			// delivers: httptest/net-url must not have already cleaned this
			// for us, or the test would be exercising nothing.
			if req.URL.Path != p {
				t.Fatalf("test setup invalid: httptest.NewRequest cleaned %q to %q before Auth ever saw it", p, req.URL.Path)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("dirty path %q was not rejected: got %d, want 401", p, rr.Code)
			}
		})
	}
}

func TestAuthSkipsOnlyDeclaredPaths(t *testing.T) {
	cfg := DefaultAuthConfig()
	cfg.APIKey = "secret"

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		path     string
		wantCode int
		why      string
	}{
		{"/health", http.StatusOK, "declared public"},
		{"/api/v1/topology", http.StatusOK, "declared public"},
		{"/api/v1/topology/export", http.StatusOK, "declared public"},
		{"/api/v1/topology/history/range", http.StatusOK, "declared public"},
		{"/api/v1/topology/history/stale", http.StatusOK, "declared public"},
		{"/api/v1/topology/history/diff", http.StatusOK, "declared public"},
		{"/api/v1/services", http.StatusOK, "declared public"},
		{"/api/v1/services/10.0.0.1%2Fnginx", http.StatusOK, "covered by the services/ prefix"},
		{"/api/v1/services/10.0.0.1%2Fnginx/impact", http.StatusOK, "covered by the services/ prefix"},
		{"/api/v1/graph/criticality", http.StatusOK, "declared public"},
		{"/api/v1/graph/orphans", http.StatusOK, "declared public"},

		{"/api/v1/events", http.StatusUnauthorized, "ingest endpoint"},
		{"/api/v1/inspection", http.StatusUnauthorized, "ingest endpoint"},
		{"/api/v1/ai/config", http.StatusUnauthorized, "must not inherit from /api/v1/ai/status"},
		{"/api/v1/ai/ask", http.StatusUnauthorized, "must not inherit from /api/v1/ai/status"},
		{"/api/v1/version", http.StatusUnauthorized, "not declared public"},
		{"/api/v1/topology-admin", http.StatusUnauthorized, "sibling of a public path, not beneath it"},
		{"/api/v1/services-admin", http.StatusUnauthorized, "sibling of a public path, not beneath it"},
		{"/healthz-internal", http.StatusUnauthorized, "sibling of a public path, not beneath it"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tc.wantCode {
				t.Errorf("%s (%s): got %d, want %d", tc.path, tc.why, rr.Code, tc.wantCode)
			}
		})
	}
}

// TestAuthAcceptsValidKey confirms the happy path still works for a protected
// route, via both supported header forms.
func TestAuthAcceptsValidKey(t *testing.T) {
	cfg := DefaultAuthConfig()
	cfg.APIKey = "secret"

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for name, set := range map[string]func(*http.Request){
		"X-API-Key":     func(r *http.Request) { r.Header.Set("X-API-Key", "secret") },
		"Bearer token":  func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") },
		"wrong key":     func(r *http.Request) { r.Header.Set("X-API-Key", "nope") },
		"no credential": func(r *http.Request) {},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/events", nil)
			set(req)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			wantOK := name == "X-API-Key" || name == "Bearer token"
			if wantOK && rr.Code != http.StatusOK {
				t.Errorf("expected 200 for %s, got %d", name, rr.Code)
			}
			if !wantOK && rr.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 for %s, got %d", name, rr.Code)
			}
		})
	}
}

// TestBodyLimit checks that an oversized body fails rather than being read
// into memory in full.
func TestBodyLimit(t *testing.T) {
	const limit = 1024

	var readErr error
	handler := BodyLimit(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	t.Run("under the limit is read", func(t *testing.T) {
		readErr = nil
		req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", limit-1)))
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if readErr != nil {
			t.Errorf("expected body to be readable, got %v", readErr)
		}
	})

	t.Run("over the limit errors", func(t *testing.T) {
		readErr = nil
		req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", limit*4)))
		handler.ServeHTTP(httptest.NewRecorder(), req)
		if readErr == nil {
			t.Error("expected an error reading an oversized body, got nil")
		}
	})

	t.Run("non-positive limit disables the middleware", func(t *testing.T) {
		readErr = nil
		passthrough := BodyLimit(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
		}))
		req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", limit*4)))
		passthrough.ServeHTTP(httptest.NewRecorder(), req)
		if readErr != nil {
			t.Errorf("expected no limit to be applied, got %v", readErr)
		}
	})
}

// TestCORSWildcardDropsCredentials verifies we never emit the
// "Access-Control-Allow-Origin: *" + "Access-Control-Allow-Credentials: true"
// pair, which browsers reject outright.
func TestCORSWildcardDropsCredentials(t *testing.T) {
	handler := CORS(CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	origin := rr.Header().Get("Access-Control-Allow-Origin")
	creds := rr.Header().Get("Access-Control-Allow-Credentials")

	if origin == "*" && creds == "true" {
		t.Error("emitted the wildcard-origin + credentials combination browsers reject")
	}
	if creds == "true" {
		t.Errorf("credentials should be dropped for a wildcard origin, got %q", creds)
	}
}

// TestCORSExplicitOriginKeepsCredentials confirms the guard only applies to
// the wildcard case.
func TestCORSExplicitOriginKeepsCredentials(t *testing.T) {
	handler := CORS(CORSConfig{
		AllowedOrigins:   []string{"https://app.example"},
		AllowedMethods:   []string{"GET"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://app.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("expected credentials to be allowed for an explicit origin, got %q", got)
	}
}
