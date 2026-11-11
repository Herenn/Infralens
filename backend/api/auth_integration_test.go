package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/config"
	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// End-to-end tests of UI login through the real router and middleware chain
// (Recovery -> Metrics -> Logging -> BodyLimit -> CORS -> UIAuth), served over
// a real listener so cookies, redirects and WebSocket upgrades behave as they
// do in production.

const (
	testAPIKey   = "agent-api-key"
	testPassword = "correct horse battery staple"
)

type authEnv struct {
	server  *Server
	srv     *httptest.Server
	stopped bool
}

// stop shuts the server down (a "restart" is stop + a new env on the same
// store). Idempotent.
func (e *authEnv) stop() {
	if e.stopped {
		return
	}
	e.stopped = true
	e.srv.CloseClientConnections()
	e.srv.Close()
	e.server.Close()
}

func newAuthEnv(t *testing.T, password string) *authEnv {
	t.Helper()
	return newAuthEnvWith(t, envOptions{password: password})
}

type envOptions struct {
	password      string
	sessionSecret string
	// store is shared with other envs (restart / multi-replica tests) and is
	// then not closed by this env.
	store       storage.Store
	corsOrigins []string
	corsCreds   bool
}

func newAuthEnvWith(t *testing.T, o envOptions) *authEnv {
	t.Helper()

	store := o.store
	ownStore := store == nil
	if ownStore {
		var err error
		store, err = sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true})
		if err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	origins := o.corsOrigins
	if origins == nil {
		origins = []string{"*"}
	}

	cfg := &config.Config{
		Server: config.ServerConfig{MaxRequestBytes: 1 << 20},
		CORS: middleware.CORSConfig{
			AllowedOrigins:   origins,
			AllowedMethods:   []string{"GET", "POST"},
			AllowedHeaders:   []string{"Content-Type", "Authorization", "X-API-Key"},
			AllowCredentials: o.corsCreds,
		},
		Auth: middleware.DefaultAuthConfig(),
	}
	cfg.Auth.APIKey = testAPIKey
	cfg.Auth.UIPassword = o.password
	cfg.Auth.SessionSecret = o.sessionSecret

	s := NewServer(cfg, store, nil, llm.NewManager(nil))
	// A route nobody registered with the auth layer, standing in for the
	// endpoints other 3.1 features add: it must be protected by default.
	s.router.HandleFunc("/api/v1/brand-new-endpoint", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(s.Handler())
	e := &authEnv{server: s, srv: srv}
	t.Cleanup(func() {
		e.stop()
		if ownStore {
			store.Close()
		}
	})
	return e
}

// client returns an HTTP client with its own cookie jar that does not follow
// redirects (so a router redirect is observed, not silently chased).
func (e *authEnv) client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (e *authEnv) do(t *testing.T, c *http.Client, method, path string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *authEnv) login(t *testing.T, c *http.Client, user, pass string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return e.do(t, c, "POST", "/api/v1/auth/login", body, map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "same-origin",
	})
}

func (e *authEnv) status(t *testing.T, c *http.Client) map[string]interface{} {
	t.Helper()
	resp := e.do(t, c, "GET", "/api/v1/auth/status", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return out
}

func (e *authEnv) wsURL() string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/ws"
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == middleware.SessionCookieName {
			return c
		}
	}
	t.Fatal("login response set no session cookie")
	return nil
}

var readEndpoints = []string{
	"/api/v1/topology",
	"/api/v1/topology/export?format=mermaid",
	"/api/v1/services",
	"/api/v1/services/10.0.0.1/nginx",
	"/api/v1/graph/stats",
	"/api/v1/graph/criticality",
	"/api/v1/graph/orphans",
	"/api/v1/graph/impact?service=x",
	"/api/v1/topology/history/range",
	"/api/v1/ai/status",
	"/api/v1/ai/providers",
	"/api/v1/version",
	"/api/v1/brand-new-endpoint",
}

func TestAuthE2E_PasswordSet_UnauthenticatedReadsRejected(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	for _, p := range readEndpoints {
		if resp := e.do(t, c, "GET", p, nil, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s: got %d, want 401", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/api/v1/ai/config", "/api/v1/ai/ask?serviceId=x", "/api/v1/ai/docs?serviceId=x"} {
		resp := e.do(t, c, "POST", p, []byte(`{}`), map[string]string{"Content-Type": "application/json"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("anonymous POST %s: got %d, want 401", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/health", "/ready"} {
		if resp := e.do(t, c, "GET", p, nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200 (must stay open for probes)", p, resp.StatusCode)
		}
	}

	st := e.status(t, c)
	if st["login_required"] != true || st["authenticated"] != false {
		t.Errorf("anonymous status = %v, want login_required=true authenticated=false", st)
	}
	if _, leaked := st["username"]; leaked {
		t.Error("status leaked the username to an anonymous caller")
	}
}

func TestAuthE2E_PasswordSet_SessionAccepted(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	resp := e.login(t, c, "admin", testPassword)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d", resp.StatusCode)
	}
	ck := sessionCookie(t, resp)
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure {
		t.Errorf("cookie attrs: HttpOnly=%v SameSite=%v Secure=%v; want true/Strict/false over plain http",
			ck.HttpOnly, ck.SameSite, ck.Secure)
	}

	for _, p := range readEndpoints {
		if resp := e.do(t, c, "GET", p, nil, nil); resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Errorf("GET %s with session: got %d", p, resp.StatusCode)
		}
	}
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/topology with session: got %d, want 200", resp.StatusCode)
	}

	// State-changing request with the cookie from our own origin.
	resp = e.do(t, c, "POST", "/api/v1/ai/config", []byte(`{}`), map[string]string{
		"Content-Type": "application/json", "Origin": e.srv.URL,
	})
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("same-origin POST /ai/config with session: got %d", resp.StatusCode)
	}
	// ...and from a foreign one.
	resp = e.do(t, c, "POST", "/api/v1/ai/config", []byte(`{}`), map[string]string{
		"Content-Type": "application/json", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST /ai/config with session: got %d, want 403", resp.StatusCode)
	}

	st := e.status(t, c)
	if st["authenticated"] != true || st["method"] != "session" || st["username"] != "admin" || st["expires_at"] == nil {
		t.Errorf("status with session = %v", st)
	}
}

func TestAuthE2E_PasswordSet_APIKeyStillAccepted(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	for _, p := range readEndpoints {
		for name, hdr := range map[string]map[string]string{
			"X-API-Key": {"X-API-Key": testAPIKey},
			"Bearer":    {"Authorization": "Bearer " + testAPIKey},
		} {
			if resp := e.do(t, c, "GET", p, nil, hdr); resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("GET %s with %s: got 401", p, name)
			}
		}
	}

	// Agent ingestion: exactly as before, API key required and sufficient.
	batch := []byte(`{"node_name":"n1","timestamp":"2026-11-01T00:00:00Z","events":[]}`)
	if resp := e.do(t, c, "POST", "/api/v1/events", batch, map[string]string{"X-API-Key": testAPIKey}); resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("agent ingest with API key: got 401")
	}
	if resp := e.do(t, c, "POST", "/api/v1/events", batch, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("agent ingest without API key: got %d, want 401", resp.StatusCode)
	}

	// A browser session is not an agent credential.
	e.login(t, c, "admin", testPassword)
	if resp := e.do(t, c, "POST", "/api/v1/events", batch, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("agent ingest with only a session: got %d, want 401", resp.StatusCode)
	}

	// Wrong key is rejected even with a valid session alongside it.
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, map[string]string{"X-API-Key": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong API key: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthE2E_PasswordSet_BadPassword(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	for name, creds := range map[string][2]string{
		"wrong password": {"admin", "nope"},
		"wrong username": {"root", testPassword},
		"empty":          {"", ""},
	} {
		resp := e.login(t, c, creds[0], creds[1])
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, resp.StatusCode)
		}
		for _, ck := range resp.Cookies() {
			if ck.Name == middleware.SessionCookieName && ck.Value != "" {
				t.Errorf("%s: failed login set a session cookie", name)
			}
		}
	}
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after failed logins: got %d, want 401", resp.StatusCode)
	}

	// Not JSON (e.g. a cross-site HTML form): refused outright.
	resp := e.do(t, c, "POST", "/api/v1/auth/login", []byte("username=admin&password=x"), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("form-encoded login: got %d, want 415", resp.StatusCode)
	}

	// Cross-site login attempt: refused before credentials are checked.
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": testPassword})
	resp = e.do(t, c, "POST", "/api/v1/auth/login", body, map[string]string{
		"Content-Type": "application/json", "Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site login: got %d, want 403", resp.StatusCode)
	}
}

func TestAuthE2E_PasswordSet_LoginThrottled(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	var throttled *http.Response
	for i := 0; i < 50 && throttled == nil; i++ {
		resp := e.login(t, c, "admin", "guess")
		switch resp.StatusCode {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			throttled = resp
			if i > 10 {
				t.Errorf("throttling kicked in only after %d failures", i)
			}
		default:
			t.Fatalf("attempt %d: unexpected %d", i, resp.StatusCode)
		}
	}
	if throttled == nil {
		t.Fatal("50 consecutive bad passwords were never throttled")
	}
	if ra := throttled.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("throttled response Retry-After = %q, want a positive number of seconds", ra)
	}

	// While throttled, even the right password is refused - otherwise the
	// throttle would still be a password oracle.
	if resp := e.login(t, c, "admin", testPassword); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("correct password while throttled: got %d, want 429", resp.StatusCode)
	}

	// The API key is unaffected by login throttling.
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, map[string]string{"X-API-Key": testAPIKey}); resp.StatusCode != http.StatusOK {
		t.Errorf("API key while login is throttled: got %d, want 200", resp.StatusCode)
	}
}

func TestAuthE2E_PasswordSet_LogoutInvalidates(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	ck := sessionCookie(t, e.login(t, c, "admin", testPassword))
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("before logout: got %d", resp.StatusCode)
	}

	resp := e.do(t, c, "POST", "/api/v1/auth/logout", nil, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204", resp.StatusCode)
	}
	cleared := false
	for _, rc := range resp.Cookies() {
		if rc.Name == middleware.SessionCookieName && rc.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the session cookie")
	}
	if resp := e.do(t, c, "GET", "/api/v1/topology", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout (jar): got %d, want 401", resp.StatusCode)
	}

	// The token itself is dead server-side, not just dropped by the browser.
	replay := e.client(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/topology", nil)
	req.AddCookie(ck)
	r2, err := replay.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("replayed cookie after logout: got %d, want 401", r2.StatusCode)
	}

	// Logging out again, or without a session, is harmless.
	if resp := e.do(t, e.client(t), "POST", "/api/v1/auth/logout", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("logout without session: got %d, want 204", resp.StatusCode)
	}
}

func TestAuthE2E_PasswordSet_WebSocket(t *testing.T) {
	e := newAuthEnv(t, testPassword)

	// No session: the handshake is refused with a 401, before any upgrade.
	_, resp, err := websocket.DefaultDialer.Dial(e.wsURL(), nil)
	if err == nil {
		t.Fatal("WebSocket without a session connected")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("WebSocket without a session: got %v, want 401", resp)
	}

	c := e.client(t)
	ck := sessionCookie(t, e.login(t, c, "admin", testPassword))
	withCookie := func(origin string) http.Header {
		h := http.Header{}
		h.Set("Cookie", ck.Name+"="+ck.Value)
		h.Set("Origin", origin)
		return h
	}

	// Session from a foreign origin: cross-site WebSocket hijacking, refused.
	_, resp, err = websocket.DefaultDialer.Dial(e.wsURL(), withCookie("https://evil.example"))
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket with session: err=%v resp=%v, want 403", err, resp)
	}

	// API key (non-browser client): accepted regardless of origin.
	hk := http.Header{}
	hk.Set("X-API-Key", testAPIKey)
	keyConn, _, err := websocket.DefaultDialer.Dial(e.wsURL(), hk)
	if err != nil {
		t.Fatalf("WebSocket with API key: %v", err)
	}
	defer keyConn.Close()

	// Session from our own origin: connects and receives the snapshot.
	conn, _, err := websocket.DefaultDialer.Dial(e.wsURL(), withCookie(e.srv.URL))
	if err != nil {
		t.Fatalf("same-origin WebSocket with session: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "snapshot" {
		t.Fatalf("first message: %v %q, want snapshot", err, msg.Type)
	}

	// Logging out ends the live stream, not just future handshakes.
	if resp := e.do(t, c, "POST", "/api/v1/auth/logout", nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d", resp.StatusCode)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	for {
		if err := conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Errorf("stream ended with %v, want a policy-violation close frame", err)
			}
			break
		}
	}

	// The API-key stream is untouched by the session's logout.
	if err := keyConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if err := keyConn.ReadJSON(&msg); err != nil {
		t.Errorf("API-key WebSocket after session logout: %v", err)
	}

	// And the logged-out cookie can no longer open a socket.
	_, resp, err = websocket.DefaultDialer.Dial(e.wsURL(), withCookie(e.srv.URL))
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("WebSocket with logged-out cookie: err=%v resp=%v, want 401", err, resp)
	}
}

// Path tricks through the real router: none may reach a protected handler.
// gorilla/mux redirects dirty paths, so anything other than 401 must be that
// redirect, never content.
func TestAuthE2E_PasswordSet_PathTricks(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)

	for _, p := range []string{
		"/health/../api/v1/topology",
		"/api/v1/auth/status/../../v1/topology",
		"/api/v1/services/x/../../../health",
		"/api/v1/events/../topology",
		"/api/v1//topology",
		"/api/v1/auth/status/",
		"/health%2F..%2Fapi%2Fv1%2Ftopology",
	} {
		req, err := http.NewRequest("GET", e.srv.URL+p, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Send the path verbatim: net/http would otherwise leave it alone
		// anyway, but be explicit that no client-side cleaning happens.
		req.URL.Opaque = p
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d (%q), want 401", p, resp.StatusCode, body)
		}
	}
}

func TestAuthE2E_PasswordUnset_UnchangedFromV30(t *testing.T) {
	e := newAuthEnv(t, "")
	c := e.client(t)

	// Reads public, as in v3.0.
	for _, p := range []string{
		"/api/v1/topology", "/api/v1/services", "/api/v1/graph/stats",
		"/api/v1/topology/history/range", "/api/v1/ai/status", "/health",
	} {
		if resp := e.do(t, c, "GET", p, nil, nil); resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("GET %s without password: got 401, want public as in v3.0", p)
		}
	}
	// WebSocket public, as in v3.0.
	conn, _, err := websocket.DefaultDialer.Dial(e.wsURL(), nil)
	if err != nil {
		t.Fatalf("WebSocket without password: %v", err)
	}
	conn.Close()

	// Non-declared routes still need the API key, as in v3.0.
	for _, p := range []string{"/api/v1/version", "/api/v1/brand-new-endpoint"} {
		if resp := e.do(t, c, "GET", p, nil, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without key: got %d, want 401 (v3.0 rule)", p, resp.StatusCode)
		}
		if resp := e.do(t, c, "GET", p, nil, map[string]string{"X-API-Key": testAPIKey}); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with key: got %d, want 200", p, resp.StatusCode)
		}
	}
	if resp := e.do(t, c, "POST", "/api/v1/ai/config", []byte(`{}`), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /ai/config without key: got %d, want 401 (v3.0 rule)", resp.StatusCode)
	}

	// The UI can learn that no login is needed, even though API_KEY is set.
	st := e.status(t, c)
	if st["login_required"] != false {
		t.Errorf("status without password = %v, want login_required=false", st)
	}
	if resp := e.login(t, c, "admin", "anything"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("login without password configured: got %d, want 404", resp.StatusCode)
	}
}
