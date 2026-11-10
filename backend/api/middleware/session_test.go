package middleware

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// fakeClock is a settable clock for sessions and throttling.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testAuthConfig() AuthConfig {
	cfg := DefaultAuthConfig()
	cfg.APIKey = "agent-key"
	cfg.UIPassword = "correct horse battery staple"
	return cfg
}

func newTestSessions(t *testing.T, cfg AuthConfig, clock *fakeClock) *SessionManager {
	t.Helper()
	return newTestSessionsWithStore(t, cfg, clock, newFakeRevocationStore(clock))
}

func newTestSessionsWithStore(t *testing.T, cfg AuthConfig, clock *fakeClock, store storage.SessionRevocationRepository) *SessionManager {
	t.Helper()
	sm := newSessionManager(cfg, []string{"https://trusted.example"}, store, clock.Now)
	if sm == nil {
		t.Fatal("expected a session manager with UIPassword set")
	}
	return sm
}

// loginCookie issues a session through sm and returns its cookie.
func loginCookie(t *testing.T, sm *SessionManager) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	sm.NewSession(rr, httptest.NewRequest("POST", "/api/v1/auth/login", nil))
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func requestWithCookie(c *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/topology", nil)
	r.AddCookie(c)
	return r
}

func TestNewSessionManager_DisabledWithoutPassword(t *testing.T) {
	cfg := testAuthConfig()
	cfg.UIPassword = ""
	cfg.SessionSecret = strings.Repeat("s", 40)
	if sm := NewSessionManager(cfg, nil, nil); sm != nil {
		t.Fatal("expected nil session manager (UI login disabled) without UIPassword")
	}
}

func TestAuthConfigValidate_SessionSettings(t *testing.T) {
	base := AuthConfig{APIKey: "k", UIPassword: "long-enough-password"}

	short := base
	short.SessionSecret = "too-short"
	if short.Validate() == nil {
		t.Error("expected an error for a SESSION_SECRET under 32 characters")
	}

	long := base
	long.SessionSecret = strings.Repeat("x", 32)
	if err := long.Validate(); err != nil {
		t.Errorf("32-character SESSION_SECRET rejected: %v", err)
	}

	neg := base
	neg.SessionTTL = -time.Minute
	if neg.Validate() == nil {
		t.Error("expected an error for a negative SESSION_TTL")
	}
}

func TestSession_RoundTrip(t *testing.T) {
	clock := newFakeClock()
	sm := newTestSessions(t, testAuthConfig(), clock)
	c := loginCookie(t, sm)

	s, ok := sm.SessionFromRequest(requestWithCookie(c))
	if !ok {
		t.Fatal("freshly issued session was rejected")
	}
	if want := clock.Now().Add(DefaultSessionTTL); !s.Expires.Equal(want) {
		t.Errorf("expiry = %v, want %v", s.Expires, want)
	}
}

func TestSession_CookieAttributes(t *testing.T) {
	sm := newTestSessions(t, testAuthConfig(), newFakeClock())

	cases := []struct {
		name       string
		setup      func(r *http.Request)
		wantSecure bool
	}{
		{"plain http", func(r *http.Request) {}, false},
		{"direct TLS", func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, true},
		{"behind TLS proxy", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"proxy chain", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "HTTPS, http") }, true},
		{"proxy says http", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			tc.setup(r)
			rr := httptest.NewRecorder()
			sm.NewSession(rr, r)
			cookies := rr.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("expected one cookie, got %d", len(cookies))
			}
			c := cookies[0]
			if !c.HttpOnly {
				t.Error("cookie must be HttpOnly")
			}
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("SameSite = %v, want Strict", c.SameSite)
			}
			if c.Path != "/" {
				t.Errorf("Path = %q, want /", c.Path)
			}
			if c.MaxAge != int(DefaultSessionTTL/time.Second) {
				t.Errorf("MaxAge = %d, want %d", c.MaxAge, int(DefaultSessionTTL/time.Second))
			}
			if c.Secure != tc.wantSecure {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.wantSecure)
			}
		})
	}
}

func TestSession_Expires(t *testing.T) {
	clock := newFakeClock()
	cfg := testAuthConfig()
	cfg.SessionTTL = time.Hour
	sm := newTestSessions(t, cfg, clock)
	c := loginCookie(t, sm)

	clock.Advance(59 * time.Minute)
	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); !ok {
		t.Fatal("session rejected before its TTL")
	}
	clock.Advance(time.Minute)
	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); ok {
		t.Fatal("session accepted at/after its TTL")
	}
}

func TestSession_RejectsTamperedAndForeignTokens(t *testing.T) {
	clock := newFakeClock()
	sm := newTestSessions(t, testAuthConfig(), clock)
	good := loginCookie(t, sm).Value
	parts := strings.Split(good, ".")

	// A session from another process (different random key) must not work.
	other := newTestSessions(t, testAuthConfig(), clock)
	foreign := loginCookie(t, other).Value

	farFuture := parts[0] + "." + parts[1] + ".9999999999." + parts[3]
	otherID := parts[0] + ".AAAAAAAAAAAAAAAAAAAAAAAA." + parts[2] + "." + parts[3]

	for name, v := range map[string]string{
		"extended expiry":     farFuture,
		"swapped session id":  otherID,
		"truncated signature": good[:len(good)-2],
		"no signature":        parts[0] + "." + parts[1] + "." + parts[2],
		"wrong version":       "v0." + parts[1] + "." + parts[2] + "." + parts[3],
		"garbage":             "not-a-token",
		"empty":               "",
		"foreign key":         foreign,
	} {
		t.Run(name, func(t *testing.T) {
			c := &http.Cookie{Name: SessionCookieName, Value: v}
			if _, ok := sm.SessionFromRequest(requestWithCookie(c)); ok {
				t.Errorf("accepted %s token %q", name, v)
			}
		})
	}
}

// With a shared SESSION_SECRET, a session issued by one process is valid on
// another (restarts, replicas) - and changing the password ends it.
func TestSession_SharedSecretAndPasswordRotation(t *testing.T) {
	clock := newFakeClock()
	cfg := testAuthConfig()
	cfg.SessionSecret = strings.Repeat("k", 32)

	a := newTestSessions(t, cfg, clock)
	b := newTestSessions(t, cfg, clock)
	c := loginCookie(t, a)
	if _, ok := b.SessionFromRequest(requestWithCookie(c)); !ok {
		t.Fatal("session from replica A rejected by replica B sharing SESSION_SECRET")
	}

	rotated := cfg
	rotated.UIPassword = "a new password entirely"
	r := newTestSessions(t, rotated, clock)
	if _, ok := r.SessionFromRequest(requestWithCookie(c)); ok {
		t.Fatal("session survived a UI_PASSWORD change")
	}

	renamed := cfg
	renamed.UIUsername = "root"
	if _, ok := newTestSessions(t, renamed, clock).SessionFromRequest(requestWithCookie(c)); ok {
		t.Fatal("session survived a UI_USERNAME change")
	}
}

func TestSession_RevokeAndBind(t *testing.T) {
	sm := newTestSessions(t, testAuthConfig(), newFakeClock())
	c := loginCookie(t, sm)
	s, ok := sm.SessionFromRequest(requestWithCookie(c))
	if !ok {
		t.Fatal("session rejected")
	}

	ctx, release := sm.Bind(context.Background(), s)
	defer release()
	if err := sm.Revoke(context.Background(), s); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, ok := sm.SessionFromRequest(requestWithCookie(c)); ok {
		t.Error("revoked session still accepted")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("context bound to a revoked session was not cancelled")
	}
	if len(sm.watchers) != 0 {
		t.Errorf("watchers not cleaned up after revoke: %d", len(sm.watchers))
	}

	// Another session is unaffected.
	other := loginCookie(t, sm)
	if _, ok := sm.SessionFromRequest(requestWithCookie(other)); !ok {
		t.Error("revoking one session affected another")
	}
}

func TestSession_BindReleaseCleansUp(t *testing.T) {
	sm := newTestSessions(t, testAuthConfig(), newFakeClock())
	s, _ := sm.SessionFromRequest(requestWithCookie(loginCookie(t, sm)))
	_, release := sm.Bind(context.Background(), s)
	release()
	if len(sm.watchers) != 0 {
		t.Errorf("watchers not cleaned up after release: %d", len(sm.watchers))
	}
}

func TestSession_RevocationListBounded(t *testing.T) {
	clock := newFakeClock()
	sm := newTestSessions(t, testAuthConfig(), clock)
	keep := loginCookie(t, sm)

	for i := 0; i < maxRevoked; i++ {
		sm.revoked["filler-"+strconv.Itoa(i)] = clock.Now().Add(time.Hour)
	}
	s, _ := sm.SessionFromRequest(requestWithCookie(loginCookie(t, sm)))
	_ = sm.Revoke(context.Background(), s) // overflows: rotates the key instead of growing the list

	if len(sm.revoked) != 0 {
		t.Errorf("revocation list not reset after rotation: %d entries", len(sm.revoked))
	}
	if _, ok := sm.SessionFromRequest(requestWithCookie(keep)); ok {
		t.Error("key rotation should have ended every session")
	}
	if _, ok := sm.SessionFromRequest(requestWithCookie(loginCookie(t, sm))); !ok {
		t.Error("new sessions must work after rotation")
	}
}

func TestCheckCredentials(t *testing.T) {
	cfg := testAuthConfig()
	sm := newTestSessions(t, cfg, newFakeClock())

	cases := []struct {
		user, pass string
		want       bool
	}{
		{"admin", cfg.UIPassword, true},
		{"admin", "wrong", false},
		{"admin", "", false},
		{"admin", cfg.UIPassword + " ", false},
		{"Admin", cfg.UIPassword, false},
		{"", cfg.UIPassword, false},
		{"root", cfg.UIPassword, false},
	}
	for _, tc := range cases {
		if got := sm.CheckCredentials(tc.user, tc.pass); got != tc.want {
			t.Errorf("CheckCredentials(%q, %q) = %v, want %v", tc.user, tc.pass, got, tc.want)
		}
	}

	custom := cfg
	custom.UIUsername = "ops"
	smc := newTestSessions(t, custom, newFakeClock())
	if !smc.CheckCredentials("ops", cfg.UIPassword) || smc.CheckCredentials("admin", cfg.UIPassword) {
		t.Error("UI_USERNAME not honoured")
	}
}

func TestLoginLimiter_PerClient(t *testing.T) {
	clock := newFakeClock()
	l := newLoginLimiter(clock.Now)

	for i := 0; i < loginClientBurst; i++ {
		if ok, _ := l.take("1.2.3.4"); !ok {
			t.Fatalf("attempt %d refused within burst", i+1)
		}
	}
	ok, retry := l.take("1.2.3.4")
	if ok {
		t.Fatal("attempt beyond burst allowed")
	}
	if retry <= 0 || retry > loginClientRefill {
		t.Errorf("retryAfter = %v, want (0, %v]", retry, loginClientRefill)
	}

	// Another client is unaffected.
	if ok, _ := l.take("5.6.7.8"); !ok {
		t.Error("a different client was throttled")
	}

	// One refill period later, exactly one more attempt.
	clock.Advance(loginClientRefill)
	if ok, _ := l.take("1.2.3.4"); !ok {
		t.Error("no attempt after a refill period")
	}
	if ok, _ := l.take("1.2.3.4"); ok {
		t.Error("more than one attempt after a single refill period")
	}
}

func TestLoginLimiter_RefundOnSuccess(t *testing.T) {
	l := newLoginLimiter(newFakeClock().Now)
	for i := 0; i < loginClientBurst*3; i++ {
		ok, _ := l.take("1.2.3.4")
		if !ok {
			t.Fatalf("successful logins exhausted the limiter at attempt %d", i+1)
		}
		l.refund("1.2.3.4")
	}
}

// The global bucket is a safety cap on distributed guessing, far above what
// any single client can reach: one client is throttled at its own burst while
// the cap still has almost all of its capacity.
func TestLoginLimiter_GlobalSafetyCap(t *testing.T) {
	l := newLoginLimiter(newFakeClock().Now)
	if loginGlobalBurst < 10*loginClientBurst {
		t.Fatalf("global cap (%d) must be far above the per-client burst (%d)", loginGlobalBurst, loginClientBurst)
	}
	for i := 0; i < loginClientBurst; i++ {
		l.take("one-client")
	}
	if ok, _ := l.take("one-client"); ok {
		t.Fatal("per-client limit not enforced")
	}
	if l.global.tokens < loginGlobalBurst-loginClientBurst-1 {
		t.Errorf("one client drained the global cap to %.0f", l.global.tokens)
	}

	// Spread across enough clients, the cap does bind.
	for i := 0; i < loginGlobalBurst; i++ {
		l.take("distributed-" + strconv.Itoa(i))
	}
	if ok, _ := l.take("yet-another"); ok {
		t.Error("global safety cap not enforced across many clients")
	}
}

func TestLoginLimiter_BoundedMemory(t *testing.T) {
	clock := newFakeClock()
	l := newLoginLimiter(clock.Now)
	l.mu.Lock()
	defer l.mu.Unlock()
	// Active clients (partly spent buckets) cannot be swept.
	for i := 0; i < loginMaxClients+50; i++ {
		if b := l.client("c"+strconv.Itoa(i), clock.Now()); b != nil {
			b.tokens = 0
		}
	}
	if len(l.clients) > loginMaxClients {
		t.Errorf("client map grew past its bound: %d", len(l.clients))
	}
	// Once they have all refilled, they are swept to make room.
	clock.Advance(loginClientRefill * loginClientBurst)
	if b := l.client("newcomer", clock.Now()); b == nil {
		t.Error("idle clients were not swept to make room")
	}
	if len(l.clients) != 1 {
		t.Errorf("expected only the newcomer after the sweep, got %d entries", len(l.clients))
	}
}

func TestAddrKey(t *testing.T) {
	cases := map[string]string{
		"192.0.2.1":            "192.0.2.1",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"not-an-ip":            "not-an-ip",
	}
	for in, want := range cases {
		if got := addrKey(in); got != want {
			t.Errorf("addrKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	sm := newTestSessions(t, testAuthConfig(), newFakeClock())

	cases := []struct {
		name         string
		host         string
		origin       string
		secFetchSite string
		want         bool
	}{
		{"sec-fetch same-origin", "infralens.example", "https://infralens.example", "same-origin", true},
		{"sec-fetch none (typed URL)", "infralens.example", "", "none", true},
		{"sec-fetch cross-site", "infralens.example", "https://evil.example", "cross-site", false},
		{"sec-fetch same-site sibling", "infralens.example", "https://evil.infralens.example", "same-site", false},
		{"sec-fetch cross-site trusted origin", "infralens.example", "https://trusted.example", "cross-site", true},
		{"sec-fetch same-origin behind host-rewriting proxy", "backend:8080", "http://localhost:3000", "same-origin", true},
		{"origin matches host", "infralens.example", "https://infralens.example", "", true},
		{"origin matches host with port", "localhost:8080", "http://localhost:8080", "", true},
		{"origin differs", "infralens.example", "https://evil.example", "", false},
		{"origin null", "infralens.example", "null", "", false},
		{"trusted origin", "infralens.example", "https://trusted.example/", "", true},
		{"host-rewriting proxy reports X-Forwarded-Host", "backend:8080", "https://infralens.example", "xfh:infralens.example", true},
		{"X-Forwarded-Host does not match", "backend:8080", "https://evil.example", "xfh:infralens.example", false},
		{"no headers (not a browser)", "infralens.example", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/ai/config", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if fwd, ok := strings.CutPrefix(tc.secFetchSite, "xfh:"); ok {
				r.Header.Set("X-Forwarded-Host", fwd)
			} else if tc.secFetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.secFetchSite)
			}
			if got := sm.SameOrigin(r); got != tc.want {
				t.Errorf("SameOrigin = %v, want %v", got, tc.want)
			}
		})
	}

	// A wildcard CORS policy must not make every origin trusted.
	// Nor may a wildcard pattern: rs/cors honours "https://*.example.com",
	// but a sibling subdomain must not be trusted with the session cookie.
	for _, pattern := range []string{"*", "https://*.infralens.example"} {
		wild := newSessionManager(testAuthConfig(), []string{pattern}, nil, time.Now)
		r := httptest.NewRequest("GET", "/api/v1/topology", nil)
		r.Host = "infralens.example"
		r.Header.Set("Origin", "https://evil.infralens.example")
		r.Header.Set("Sec-Fetch-Site", "same-site")
		if wild.SameOrigin(r) {
			t.Errorf("CORS_ORIGINS=%s made a sibling origin trusted for cookie requests", pattern)
		}
	}
}
