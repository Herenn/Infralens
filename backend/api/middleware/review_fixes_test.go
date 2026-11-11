package middleware

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// fakeRevocationStore is an in-memory SessionRevocationRepository whose
// created_at comes from the shared fake clock, standing in for the database.
type fakeRevocationStore struct {
	mu    sync.Mutex
	clock *fakeClock
	rows  map[string]storage.SessionRevocation
	fail  error
}

func newFakeRevocationStore(clock *fakeClock) *fakeRevocationStore {
	return &fakeRevocationStore{clock: clock, rows: map[string]storage.SessionRevocation{}}
}

func (f *fakeRevocationStore) setFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

func (f *fakeRevocationStore) Revoke(_ context.Context, id string, exp time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if _, ok := f.rows[id]; !ok {
		f.rows[id] = storage.SessionRevocation{SessionID: id, ExpiresAt: exp, CreatedAt: f.clock.Now()}
	}
	return nil
}

func (f *fakeRevocationStore) List(_ context.Context, since, activeAt time.Time) ([]storage.SessionRevocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	var out []storage.SessionRevocation
	for _, r := range f.rows {
		if !r.CreatedAt.Before(since) && r.ExpiresAt.After(activeAt) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRevocationStore) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	return 0, nil
}

func sharedSecretConfig() AuthConfig {
	cfg := testAuthConfig()
	cfg.SessionSecret = strings.Repeat("s", 32)
	return cfg
}

// --- 2. Revocations survive restarts and reach other replicas -------------

func TestRevocation_SurvivesRestart(t *testing.T) {
	clock := newFakeClock()
	store := newFakeRevocationStore(clock)
	cfg := sharedSecretConfig()

	before := newTestSessionsWithStore(t, cfg, clock, store)
	c := loginCookie(t, before)
	s, ok := before.SessionFromRequest(requestWithCookie(c))
	if !ok {
		t.Fatal("session rejected")
	}
	if err := before.Revoke(context.Background(), s); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	before.Close()

	// "Restart": a fresh process with the same secret and the same database.
	after := newTestSessionsWithStore(t, cfg, clock, store)
	if _, ok := after.SessionFromRequest(requestWithCookie(c)); ok {
		t.Fatal("logged-out session came back after a restart")
	}
	// Sessions that were never logged out still work after the restart.
	other := loginCookie(t, before)
	if _, ok := after.SessionFromRequest(requestWithCookie(other)); !ok {
		t.Error("a live session did not survive the restart")
	}
}

func TestRevocation_SharedStoreTwoReplicas(t *testing.T) {
	clock := newFakeClock()
	store := newFakeRevocationStore(clock)
	cfg := sharedSecretConfig()
	a := newTestSessionsWithStore(t, cfg, clock, store)
	b := newTestSessionsWithStore(t, cfg, clock, store)

	c := loginCookie(t, a)
	s, ok := b.SessionFromRequest(requestWithCookie(c))
	if !ok {
		t.Fatal("replica B rejected a session issued by A")
	}
	// A WebSocket on replica B bound to the session.
	wsCtx, release := b.Bind(context.Background(), s)
	defer release()

	clock.Advance(time.Second)
	if err := a.Revoke(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SessionFromRequest(requestWithCookie(c)); ok {
		t.Error("replica A still accepts the session it logged out")
	}

	// B honours it at its next sync - in production within
	// revocationSyncInterval - and closes its WebSocket.
	if err := b.syncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.SessionFromRequest(requestWithCookie(c)); ok {
		t.Error("replica B still accepts a session logged out on A after syncing")
	}
	select {
	case <-wsCtx.Done():
	case <-time.After(time.Second):
		t.Error("replica B did not close the WebSocket of a session logged out on A")
	}

	// Incremental syncs keep picking up later logouts.
	c2 := loginCookie(t, a)
	s2, _ := a.SessionFromRequest(requestWithCookie(c2))
	clock.Advance(10 * time.Second)
	_ = a.Revoke(context.Background(), s2)
	_ = b.syncRevocations(context.Background())
	if _, ok := b.SessionFromRequest(requestWithCookie(c2)); ok {
		t.Error("incremental sync missed a later logout")
	}
}

func TestRevocation_FailsClosedUntilLoaded(t *testing.T) {
	clock := newFakeClock()
	store := newFakeRevocationStore(clock)
	cfg := sharedSecretConfig()
	issuer := newTestSessionsWithStore(t, cfg, clock, store)
	c := loginCookie(t, issuer)

	store.setFail(errors.New("database is down"))
	sm := newTestSessionsWithStore(t, cfg, clock, store)
	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); ok {
		t.Fatal("session accepted before revocations could be loaded")
	}

	store.setFail(nil)
	if err := sm.syncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); !ok {
		t.Error("session still rejected after revocations loaded")
	}
}

func TestRevocation_PersistFailureStillLogsOutLocally(t *testing.T) {
	clock := newFakeClock()
	store := newFakeRevocationStore(clock)
	sm := newTestSessionsWithStore(t, sharedSecretConfig(), clock, store)
	c := loginCookie(t, sm)
	s, _ := sm.SessionFromRequest(requestWithCookie(c))

	store.setFail(errors.New("write failed"))
	if err := sm.Revoke(context.Background(), s); err == nil {
		t.Error("expected the persistence error to be reported")
	}
	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); ok {
		t.Error("session still accepted locally after a logout whose persistence failed")
	}
}

// --- 1. Throttling identifies clients behind trusted proxies --------------

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test IP %q", s)
	}
	return ip
}

func TestParseTrustedProxies(t *testing.T) {
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0.1, bogus"} {
		if _, err := parseTrustedProxies(bad); err == nil {
			t.Errorf("TRUSTED_PROXIES=%q accepted", bad)
		}
	}
	pc, err := parseTrustedProxies(" 10.1.2.3 , 192.0.2.0/24,2001:db8::1 ")
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{"10.1.2.3": true, "10.1.2.4": false, "192.0.2.77": true, "2001:db8::1": true, "2001:db8::2": false} {
		if got := pc.isTrusted(parseIP(t, ip)); got != want {
			t.Errorf("isTrusted(%s) = %v, want %v", ip, got, want)
		}
	}
	if err := (AuthConfig{APIKey: "k", TrustedProxies: "nope"}).Validate(); err == nil {
		t.Error("Validate accepted an invalid TRUSTED_PROXIES")
	}
}

func TestClientKey_Modes(t *testing.T) {
	def, _ := parseTrustedProxies("")
	none, _ := parseTrustedProxies("none")
	explicit, _ := parseTrustedProxies("10.0.0.5,10.0.0.6")

	cases := []struct {
		name   string
		pc     *proxyConfig
		remote string
		xff    []string
		realIP string
		want   string
	}{
		{"direct public client ignores forged XFF", def, "203.0.113.9:5000", []string{"1.1.1.1"}, "", "203.0.113.9"},
		{"default: via private proxy, rightmost entry", def, "172.18.0.3:4000", []string{"1.1.1.1, 198.51.100.7"}, "", "198.51.100.7@172.18.0.3"},
		{"default: exactly one hop", def, "10.0.0.5:4000", []string{"198.51.100.7, 10.0.0.9"}, "", "10.0.0.9@10.0.0.5"},
		{"default: X-Real-IP fallback", def, "127.0.0.1:4000", nil, "198.51.100.7", "198.51.100.7@127.0.0.1"},
		{"default: proxy with no headers", def, "10.0.0.5:4000", nil, "", "10.0.0.5"},
		{"default: multiple XFF headers", def, "10.0.0.5:1", []string{"9.9.9.9", "198.51.100.7"}, "", "198.51.100.7@10.0.0.5"},
		{"default: IPv6 client grouped by /64", def, "[::1]:1", []string{"2001:db8:1:2::99"}, "", "2001:db8:1:2::/64@::1"},
		{"none: headers ignored", none, "10.0.0.5:4000", []string{"198.51.100.7"}, "", "10.0.0.5"},
		{"explicit: walk past listed proxies", explicit, "10.0.0.5:1", []string{"6.6.6.6, 198.51.100.7, 10.0.0.6"}, "", "198.51.100.7"},
		{"explicit: unlisted private peer is a client", explicit, "10.0.0.99:1", []string{"6.6.6.6"}, "", "10.0.0.99"},
		{"explicit: all hops trusted -> leftmost", explicit, "10.0.0.5:1", []string{"10.0.0.6"}, "", "10.0.0.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if tc.realIP != "" {
				r.Header.Set("X-Real-IP", tc.realIP)
			}
			if got := tc.pc.clientKey(r); got != tc.want {
				t.Errorf("clientKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// The reported lockout: an attacker and the admin behind the same proxy. The
// attacker sends one wrong password every 20s - exactly the per-client
// refill rate - for an hour; the admin must still be able to log in.
func TestLoginThrottle_NoLockoutBehindSharedProxy(t *testing.T) {
	clock := newFakeClock()
	sm := newTestSessions(t, testAuthConfig(), clock)

	viaProxy := func(client string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		r.RemoteAddr = "172.18.0.3:41234" // the bundled nginx on the compose network
		r.Header.Set("X-Forwarded-For", client)
		return r
	}

	attackerThrottled := false
	for elapsed := time.Duration(0); elapsed < time.Hour; elapsed += loginClientRefill {
		for i := 0; i < 3; i++ { // and a few extra tries each round
			if ok, _ := sm.AllowLoginAttempt(viaProxy("203.0.113.66")); !ok {
				attackerThrottled = true
			}
		}
		if ok, _ := sm.AllowLoginAttempt(viaProxy("198.51.100.7")); !ok {
			t.Fatalf("admin throttled after %v of attacker activity from another client behind the same proxy", elapsed)
		}
		sm.LoginSucceeded(viaProxy("198.51.100.7"))
		clock.Advance(loginClientRefill)
	}
	if !attackerThrottled {
		t.Error("the attacker itself was never throttled")
	}
}

// A client connecting directly from a private address can forge
// X-Forwarded-For in default mode, but its identities are scoped to its own
// address, so it still cannot drain the bucket of the admin behind nginx.
func TestLoginThrottle_DirectSpooferCannotTargetAdmin(t *testing.T) {
	sm := newTestSessions(t, testAuthConfig(), newFakeClock())

	forged := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	forged.RemoteAddr = "10.0.0.66:1000" // attacker pod, directly at the backend
	forged.Header.Set("X-Forwarded-For", "198.51.100.7")
	for i := 0; i < 50; i++ {
		sm.AllowLoginAttempt(forged)
	}

	admin := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	admin.RemoteAddr = "172.18.0.3:41234"
	admin.Header.Set("X-Forwarded-For", "198.51.100.7")
	if ok, _ := sm.AllowLoginAttempt(admin); !ok {
		t.Error("a direct client forging the admin's address locked the admin out")
	}
}

func TestValidate_UIPasswordLength(t *testing.T) {
	for pw, wantErr := range map[string]bool{"": false, "short": true, "1234567": true, "12345678": false, "a-long-random-password": false} {
		err := AuthConfig{APIKey: "k", UIPassword: pw}.Validate()
		if (err != nil) != wantErr {
			t.Errorf("UI_PASSWORD=%q: err=%v, wantErr=%v", pw, err, wantErr)
		}
	}
}

// --- 3. Cookie-authenticated reads get the origin check too ---------------

func TestUIAuth_CookieGETOriginChecked(t *testing.T) {
	cfg := testAuthConfig()
	// CORS allows a wildcard pattern with credentials; that must not extend
	// to the session cookie.
	sm := newSessionManager(cfg, []string{"https://*.infralens.example", "https://trusted.example"}, newFakeRevocationStore(newFakeClock()), time.Now)
	cookie := loginCookie(t, sm)

	cases := []struct {
		name    string
		host    string
		headers map[string]string
		want    int
	}{
		{"sibling subdomain credentialed GET", "infralens.example",
			map[string]string{"Origin": "https://evil.infralens.example", "Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"cross-site credentialed GET", "infralens.example",
			map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"old browser cross-origin GET", "infralens.example",
			map[string]string{"Origin": "https://evil.infralens.example"}, http.StatusForbidden},
		{"sibling no-cors GET (no Origin)", "infralens.example",
			map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"exact trusted origin", "infralens.example",
			map[string]string{"Origin": "https://trusted.example", "Sec-Fetch-Site": "cross-site"}, http.StatusOK},
		// The UI's own requests, every way browsers send them.
		{"same-origin fetch (Fetch Metadata)", "infralens.example",
			map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"same-origin fetch, older browser (no headers)", "infralens.example", nil, http.StatusOK},
		{"same-origin fetch with Origin, no Fetch Metadata", "infralens.example:3000",
			map[string]string{"Origin": "http://infralens.example:3000"}, http.StatusOK},
		{"behind Host-rewriting proxy, Fetch Metadata", "backend:8080",
			map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"behind Host-rewriting proxy, X-Forwarded-Host", "backend:8080",
			map[string]string{"Origin": "http://localhost:3000", "X-Forwarded-Host": "localhost:3000"}, http.StatusOK},
		{"top-level navigation (typed URL / bookmark)", "infralens.example",
			map[string]string{"Sec-Fetch-Site": "none"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/topology", nil)
			r.Host = tc.host
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

// --- 4. A planted cookie cannot shadow the real session -------------------

func TestSession_ShadowCookieIgnored(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())
	real := loginCookie(t, sm)

	r := httptest.NewRequest("GET", "/api/v1/topology", nil)
	// Browsers send the parent-domain cookie a sibling planted ahead of ours.
	r.Header.Add("Cookie", SessionCookieName+"=junk-from-sibling")
	r.Header.Add("Cookie", SessionCookieName+"="+real.Value)
	rr := httptest.NewRecorder()
	UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Errorf("valid session shadowed by a junk cookie: got %d, want 200", rr.Code)
	}

	// Only junk: still 401.
	r = httptest.NewRequest("GET", "/api/v1/topology", nil)
	r.Header.Add("Cookie", SessionCookieName+"=junk-1; "+SessionCookieName+"=junk-2")
	rr = httptest.NewRecorder()
	UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("only junk cookies: got %d, want 401", rr.Code)
	}
}

// --- 5. A foreign Bearer token does not break a valid session -------------

func TestUIAuth_ForeignBearerWithSession(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())
	cookie := loginCookie(t, sm)

	run := func(set func(*http.Request)) int {
		r := httptest.NewRequest("GET", "/api/v1/topology", nil)
		set(r)
		rr := httptest.NewRecorder()
		UIAuth(cfg, sm)(&okHandler{}).ServeHTTP(rr, r)
		return rr.Code
	}
	if got := run(func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("Authorization", "Bearer eyJ-token-from-an-auth-proxy")
	}); got != http.StatusOK {
		t.Errorf("valid session + foreign Bearer: got %d, want 200", got)
	}
	if got := run(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer eyJ-token-from-an-auth-proxy")
	}); got != http.StatusUnauthorized {
		t.Errorf("foreign Bearer, no session: got %d, want 401", got)
	}
	if got := run(func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("X-API-Key", "wrong")
	}); got != http.StatusUnauthorized {
		t.Errorf("valid session + wrong X-API-Key: got %d, want 401 (our own header fails loudly)", got)
	}
}

// --- 10. No bypass list ---------------------------------------------------

// UIAuth has no list of paths that skip it: /mcp, the one endpoint with its
// own authentication, is served ahead of the whole chain (api/mcp.go). Should
// a request for /mcp or a neighbour ever reach UIAuth (MCP_ENABLED=false, or a
// path withMCP does not claim), it is protected like any other route.
func TestUIAuth_NoSelfAuthenticatingBypass(t *testing.T) {
	withPw := testAuthConfig()
	sm := newTestSessions(t, withPw, newFakeClock())
	legacyCfg := DefaultAuthConfig()
	legacyCfg.APIKey = "agent-key"

	for name, mw := range map[string]func(http.Handler) http.Handler{
		"password set":   UIAuth(withPw, sm),
		"password unset": UIAuth(legacyCfg, nil),
	} {
		t.Run(name, func(t *testing.T) {
			for _, p := range []string{"/mcp", "/mcp/", "/mcp/admin", "/mcpx", "/api/v1/mcp", "/mcp/../api/v1/ai/config"} {
				rr := httptest.NewRecorder()
				mw(&okHandler{}).ServeHTTP(rr, httptest.NewRequest("POST", p, nil))
				if rr.Code != http.StatusUnauthorized {
					t.Errorf("%s: got %d, want 401", p, rr.Code)
				}
			}
		})
	}
}
