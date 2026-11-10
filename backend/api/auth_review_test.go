package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// Regression tests for the security review of UI login, through the real
// router, middleware chain and SQLite store.

var testSessionSecret = strings.Repeat("0123456789abcdef", 4)

func openFileStore(t *testing.T, path string) storage.Store {
	t.Helper()
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: path, AutoMigrate: true})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

// getWithCookie replays a raw cookie from a fresh client (no jar).
func (e *authEnv) getWithCookie(t *testing.T, path string, ck *http.Cookie, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.AddCookie(ck)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Review item 2: a logged-out cookie must stay dead across a restart with
// the same SESSION_SECRET (it used to come back: 200 after restart).
func TestReview_LogoutSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "infralens.db")

	store1 := openFileStore(t, dbPath)
	e1 := newAuthEnvWith(t, envOptions{password: testPassword, sessionSecret: testSessionSecret, store: store1})
	c := e1.client(t)
	loggedOut := sessionCookie(t, e1.login(t, c, "admin", testPassword))
	kept := sessionCookie(t, e1.login(t, e1.client(t), "admin", testPassword))
	if resp := e1.do(t, c, "POST", "/api/v1/auth/logout", nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	e1.stop()
	store1.Close()

	// Restart: new process, same database file, same secret.
	store2 := openFileStore(t, dbPath)
	t.Cleanup(func() { store2.Close() })
	e2 := newAuthEnvWith(t, envOptions{password: testPassword, sessionSecret: testSessionSecret, store: store2})

	if got := e2.getWithCookie(t, "/api/v1/topology", loggedOut, nil); got != http.StatusUnauthorized {
		t.Errorf("logged-out cookie after restart: got %d, want 401", got)
	}
	if got := e2.getWithCookie(t, "/api/v1/topology", kept, nil); got != http.StatusOK {
		t.Errorf("live cookie after restart: got %d, want 200 (sessions must survive restarts with SESSION_SECRET)", got)
	}
}

// Review item 2: two replicas sharing one database. A logout on A is honoured
// by B within the documented sync bound, and B closes its WebSocket.
func TestReview_LogoutReachesOtherReplica(t *testing.T) {
	store := openFileStore(t, filepath.Join(t.TempDir(), "shared.db"))
	t.Cleanup(func() { store.Close() })
	a := newAuthEnvWith(t, envOptions{password: testPassword, sessionSecret: testSessionSecret, store: store})
	b := newAuthEnvWith(t, envOptions{password: testPassword, sessionSecret: testSessionSecret, store: store})

	ca := a.client(t)
	ck := sessionCookie(t, a.login(t, ca, "admin", testPassword))
	if got := b.getWithCookie(t, "/api/v1/topology", ck, nil); got != http.StatusOK {
		t.Fatalf("replica B with A's session: got %d", got)
	}

	h := http.Header{}
	h.Set("Cookie", ck.Name+"="+ck.Value)
	h.Set("Origin", b.srv.URL)
	conn, _, err := websocket.DefaultDialer.Dial(b.wsURL(), h)
	if err != nil {
		t.Fatalf("WebSocket on B: %v", err)
	}
	defer conn.Close()

	if resp := a.do(t, ca, "POST", "/api/v1/auth/logout", nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout on A: %d", resp.StatusCode)
	}
	start := time.Now()

	// The WebSocket on B is closed by B's next sync.
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var msg map[string]interface{}
	for {
		if err := conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
				t.Errorf("B's WebSocket ended with %v, want a policy-violation close", err)
			}
			break
		}
	}
	took := time.Since(start)
	if took > 7*time.Second {
		t.Errorf("replica B took %v to honour the logout; documented bound is ~5s", took)
	}
	if got := b.getWithCookie(t, "/api/v1/topology", ck, nil); got != http.StatusUnauthorized {
		t.Errorf("replica B after sync: got %d, want 401", got)
	}
	t.Logf("logout on A honoured by B after %v", took.Round(10*time.Millisecond))
}

// Review item 1: the reported lockout. Admin and attacker both reach the
// backend through the same proxy (httptest connections arrive from
// 127.0.0.1, a default trusted proxy) with different X-Forwarded-For. The
// attacker exhausts its own bucket; the admin logs in regardless.
func TestReview_NoAdminLockoutBehindProxy(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	c := e.client(t)
	loginAs := func(xff, pass string) int {
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": pass})
		return e.do(t, c, "POST", "/api/v1/auth/login", body, map[string]string{
			"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "X-Forwarded-For": xff,
		}).StatusCode
	}

	throttled := false
	for i := 0; i < 20; i++ {
		if loginAs("203.0.113.66", "wrong-guess") == http.StatusTooManyRequests {
			throttled = true
		}
	}
	if !throttled {
		t.Fatal("attacker was never throttled")
	}
	if got := loginAs("198.51.100.7", testPassword); got != http.StatusOK {
		t.Errorf("admin behind the same proxy: got %d, want 200", got)
	}
	// Forging the admin's address in a header the proxy appends to does not
	// help: the proxy's own entry is the rightmost one.
	for i := 0; i < 20; i++ {
		loginAs("198.51.100.7, 203.0.113.66", "wrong-guess")
	}
	if got := loginAs("198.51.100.7", testPassword); got != http.StatusOK {
		t.Errorf("admin after attacker forged its address: got %d, want 200", got)
	}
}

// Review item 3: with a wildcard CORS pattern and credentials allowed, a
// sibling subdomain must not be able to read the topology with the victim's
// cookie (it used to get 200 with Access-Control-Allow-Origin).
func TestReview_SiblingOriginCredentialedGET(t *testing.T) {
	e := newAuthEnvWith(t, envOptions{
		password:    testPassword,
		corsOrigins: []string{"https://*.infralens.example"},
		corsCreds:   true,
	})
	ck := sessionCookie(t, e.login(t, e.client(t), "admin", testPassword))

	if got := e.getWithCookie(t, "/api/v1/topology", ck, map[string]string{
		"Origin": "https://evil.infralens.example", "Sec-Fetch-Site": "same-site",
	}); got != http.StatusForbidden {
		t.Errorf("sibling-origin credentialed GET: got %d, want 403", got)
	}
	// Status must not leak the session's details to it either.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/auth/status", nil)
	req.AddCookie(ck)
	req.Header.Set("Origin", "https://evil.infralens.example")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st["authenticated"] != false || st["username"] != nil {
		t.Errorf("status to a sibling origin = %v, want unauthenticated with no username", st)
	}
	// The UI's own reads still work.
	if got := e.getWithCookie(t, "/api/v1/topology", ck, map[string]string{"Sec-Fetch-Site": "same-origin"}); got != http.StatusOK {
		t.Errorf("same-origin GET: got %d, want 200", got)
	}
	if got := e.getWithCookie(t, "/api/v1/topology", ck, nil); got != http.StatusOK {
		t.Errorf("GET without Fetch Metadata or Origin: got %d, want 200", got)
	}
}

// Review items 4 and 5 through the full chain.
func TestReview_ShadowCookieAndForeignBearer(t *testing.T) {
	e := newAuthEnv(t, testPassword)
	ck := sessionCookie(t, e.login(t, e.client(t), "admin", testPassword))

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/topology", nil)
	req.Header.Add("Cookie", middleware.SessionCookieName+"=planted-by-sibling")
	req.Header.Add("Cookie", ck.Name+"="+ck.Value)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("shadowing junk cookie + valid cookie: got %d, want 200", resp.StatusCode)
	}

	if got := e.getWithCookie(t, "/api/v1/topology", ck, map[string]string{"Authorization": "Bearer junk-from-auth-proxy"}); got != http.StatusOK {
		t.Errorf("valid cookie + foreign Bearer: got %d, want 200", got)
	}
	r2 := e.do(t, e.client(t), "GET", "/api/v1/topology", nil, map[string]string{"Authorization": "Bearer junk-from-auth-proxy"})
	if r2.StatusCode != http.StatusUnauthorized {
		t.Errorf("foreign Bearer alone: got %d, want 401", r2.StatusCode)
	}
}

// The WebSocket handler gained a case on r.Context().Done(). With no password
// that context is the plain request context: the stream must stay open and
// keep delivering deltas, not just connect and close.
func TestReview_NoPasswordWebSocketStaysOpen(t *testing.T) {
	e := newAuthEnv(t, "")
	conn, _, err := websocket.DefaultDialer.Dial(e.wsURL(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var msg struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "snapshot" {
		t.Fatalf("first message: %v %q", err, msg.Type)
	}

	// Still open well after the handshake request "finished"...
	time.Sleep(1500 * time.Millisecond)

	// ...and still streaming: ingest an event and expect its delta.
	batch := []byte(`{"node_name":"ws-node","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","events":[` +
		`{"pid":1,"comm":"nginx","src_addr":"10.9.0.1","dst_addr":"10.9.0.2","dst_port":80,"direction":0}]}`)
	if resp := e.do(t, e.client(t), "POST", "/api/v1/events", batch, map[string]string{
		"X-API-Key": testAPIKey, "Content-Type": "application/json",
	}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest: %d", resp.StatusCode)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("stream closed or stalled after the handshake: %v", err)
		}
		if msg.Type == "service" || msg.Type == "connection" {
			return
		}
	}
}
