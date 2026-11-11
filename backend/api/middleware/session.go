package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/storage"
)

// UI login sessions.
//
// A session is a stateless, HMAC-signed cookie value:
//
//	v1.<session id>.<expiry, unix seconds>.<HMAC-SHA256 signature>
//
// Signed tokens rather than a server-side session table, so that with a
// shared SESSION_SECRET sessions survive restarts and work across replicas
// without a lookup per request. The signing key is derived from
// SESSION_SECRET (or a random per-process key when it is unset) together with
// the configured username and password, so changing UI_PASSWORD invalidates
// every outstanding session.
//
// Logout is the only server-side state. A logged-out session ID is recorded
// in the store (table ui_session_revocations) and in an in-memory set that
// every request is checked against - no database query on the hot path. Each
// process loads all unexpired revocations from the store before serving and
// then re-syncs every revocationSyncInterval, so:
//
//   - a logout is honoured immediately by the process that handled it,
//   - by every other replica sharing the database within
//     revocationSyncInterval (5s, plus the query time), at which point any
//     WebSocket bound to that session on that replica is closed too, and
//   - across restarts with no gap: the initial load happens before the
//     first request, and sessions are refused outright until it succeeds.

const (
	// SessionCookieName is the cookie carrying the UI session.
	SessionCookieName = "infralens_session"

	// DefaultSessionTTL is the absolute session lifetime when SESSION_TTL is
	// unset: a working day, after which the user signs in again.
	DefaultSessionTTL = 12 * time.Hour

	// DefaultUIUsername is the admin account name when UI_USERNAME is unset.
	DefaultUIUsername = "admin"

	tokenVersion = "v1"

	// revocationSyncInterval bounds how long a logout on one replica takes
	// to be honoured by the others.
	revocationSyncInterval = 5 * time.Second

	// revocationSyncOverlap re-reads revocations created slightly before the
	// newest one already seen. created_at is the inserting transaction's
	// start time, so with concurrent logouts on Postgres a row can become
	// visible after a newer one; the overlap makes sure it is still picked
	// up. Re-reading a known revocation is harmless.
	revocationSyncOverlap = 60 * time.Second

	// maxRevoked bounds the in-memory revocation set. Only a valid session
	// can be revoked, so filling it takes a correct password; if it does
	// fill up, the signing key is rotated instead, which ends every session
	// on this process at once.
	maxRevoked = 100000
)

// Session is a validated UI session.
type Session struct {
	ID      string
	Expires time.Time
}

// Identity is who a request authenticated as.
type Identity struct {
	// Method is "api_key", "session", or "" when unauthenticated.
	Method string
	// Session is set when Method is "session".
	Session *Session
	// BadAPIKey reports that an API key was presented but did not match.
	BadAPIKey bool
}

const (
	MethodAPIKey  = "api_key"
	MethodSession = "session"
)

// SessionManager issues and validates UI sessions, checks credentials, and
// throttles login attempts. A nil *SessionManager means UI login is disabled.
type SessionManager struct {
	username   string
	userDigest [32]byte
	passDigest [32]byte

	apiKey     string
	headerName string

	baseKey []byte
	ttl     time.Duration

	// trustedOrigins are explicit CORS origins, allowed to make
	// cookie-authenticated requests from another origin.
	trustedOrigins map[string]bool

	proxies *proxyConfig
	now     func() time.Time
	limiter *loginLimiter

	// store persists revocations; nil means memory only (a store without
	// revocation support - not the case for SQLite or Postgres).
	store     storage.SessionRevocationRepository
	stopSync  chan struct{}
	syncDone  chan struct{}
	closeOnce sync.Once

	mu         sync.Mutex
	key        []byte // current signing key, derived from baseKey + generation
	generation uint64
	revoked    map[string]time.Time
	synced     bool      // initial load from store succeeded
	syncCursor time.Time // newest created_at seen in the store
	watchers   map[string]map[uint64]context.CancelFunc
	nextWatch  uint64
}

// NewSessionManager returns the session manager for cfg, or nil when
// UIPassword is unset (UI login disabled). corsOrigins is the CORS allow-list:
// explicit entries in it are trusted for cross-origin cookie requests,
// wildcards and patterns are not. store persists logouts; it may be nil.
// Call Close when done to stop the background sync.
func NewSessionManager(cfg AuthConfig, corsOrigins []string, store storage.SessionRevocationRepository) *SessionManager {
	sm := newSessionManager(cfg, corsOrigins, store, time.Now)
	if sm != nil && sm.store != nil {
		go sm.syncLoop(revocationSyncInterval)
	}
	return sm
}

// newSessionManager builds the manager and, when there is a store, performs
// the initial revocation load - but starts no goroutine, so tests drive
// syncing explicitly with syncRevocations.
func newSessionManager(cfg AuthConfig, corsOrigins []string, store storage.SessionRevocationRepository, now func() time.Time) *SessionManager {
	if cfg.UIPassword == "" {
		if cfg.SessionSecret != "" {
			log.Warn("SESSION_SECRET is set but UI_PASSWORD is not - UI login stays disabled")
		}
		return nil
	}

	username := cfg.UIUsername
	if username == "" {
		username = DefaultUIUsername
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	headerName := cfg.HeaderName
	if headerName == "" {
		headerName = "X-API-Key"
	}
	proxies, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		// Validate rejects this at startup; fall back to trusting no one.
		log.WithError(err).Error("Invalid TRUSTED_PROXIES - not trusting any forwarding headers")
		proxies = &proxyConfig{explicit: true}
	}

	var secret []byte
	if cfg.SessionSecret != "" {
		secret = []byte(cfg.SessionSecret)
	} else {
		secret = make([]byte, 32)
		// crypto/rand.Read never returns an error as of Go 1.24; it aborts
		// the process instead of ever handing back a weak key.
		_, _ = rand.Read(secret)
		log.Info("UI login: SESSION_SECRET is not set, sessions are signed with a per-process key - " +
			"they end when the backend restarts, and with more than one backend replica you must set SESSION_SECRET")
	}
	if len(cfg.UIPassword) < recommendedUIPasswordLen {
		log.Warnf("UI_PASSWORD is shorter than %d characters - login is throttled, but use a long random password",
			recommendedUIPasswordLen)
	}

	// The password is mixed into the signing key: rotating UI_PASSWORD
	// immediately invalidates every session issued under the old one.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("infralens-ui-session-" + tokenVersion + "\x00"))
	mac.Write([]byte(username))
	mac.Write([]byte{0})
	mac.Write([]byte(cfg.UIPassword))

	sm := &SessionManager{
		username:       username,
		userDigest:     sha256.Sum256([]byte(username)),
		passDigest:     sha256.Sum256([]byte(cfg.UIPassword)),
		apiKey:         cfg.APIKey,
		headerName:     headerName,
		baseKey:        mac.Sum(nil),
		ttl:            ttl,
		trustedOrigins: make(map[string]bool),
		proxies:        proxies,
		now:            now,
		limiter:        newLoginLimiter(now),
		store:          store,
		stopSync:       make(chan struct{}),
		syncDone:       make(chan struct{}),
		revoked:        make(map[string]time.Time),
		watchers:       make(map[string]map[uint64]context.CancelFunc),
	}
	sm.key = sm.deriveKey(0)
	for _, o := range corsOrigins {
		if o = normalizeOrigin(o); o != "" && !strings.Contains(o, "*") {
			sm.trustedOrigins[o] = true
		}
	}

	if store == nil {
		log.Warn("UI login: the store cannot persist logouts - they are forgotten on restart and not shared between replicas")
		sm.synced = true
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := sm.syncRevocations(ctx); err != nil {
			log.WithError(err).Error("UI login: could not load session revocations - " +
				"refusing all sessions until the database is readable")
		}
		cancel()
	}

	log.WithFields(log.Fields{"username": username, "session_ttl": ttl}).
		Info("UI login enabled - read endpoints and the WebSocket require a session or the API key")
	return sm
}

// Close stops the background revocation sync. Safe on a nil manager and
// safe to call more than once.
func (sm *SessionManager) Close() {
	if sm == nil {
		return
	}
	sm.closeOnce.Do(func() { close(sm.stopSync) })
}

func (sm *SessionManager) syncLoop(every time.Duration) {
	defer close(sm.syncDone)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), every)
			if err := sm.syncRevocations(ctx); err != nil {
				log.WithError(err).Warn("UI login: session revocation sync failed")
			}
			cancel()
		case <-sm.stopSync:
			return
		}
	}
}

// syncRevocations pulls revocations from the store into memory, closing any
// WebSocket bound to a session revoked elsewhere. The first successful call
// loads everything unexpired; later ones only what is new.
func (sm *SessionManager) syncRevocations(ctx context.Context) error {
	if sm.store == nil {
		return nil
	}
	sm.mu.Lock()
	since := time.Unix(0, 0)
	if sm.synced && !sm.syncCursor.IsZero() {
		since = sm.syncCursor.Add(-revocationSyncOverlap)
	}
	sm.mu.Unlock()

	now := sm.now()
	rows, err := sm.store.List(ctx, since, now)
	if err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, r := range rows {
		if _, known := sm.revoked[r.SessionID]; !known {
			sm.revoked[r.SessionID] = r.ExpiresAt
			sm.cancelWatchersLocked(r.SessionID)
		}
		if r.CreatedAt.After(sm.syncCursor) {
			sm.syncCursor = r.CreatedAt
		}
	}
	for id, exp := range sm.revoked {
		if !now.Before(exp) {
			delete(sm.revoked, id)
		}
	}
	sm.synced = true
	return nil
}

func (sm *SessionManager) deriveKey(generation uint64) []byte {
	mac := hmac.New(sha256.New, sm.baseKey)
	mac.Write([]byte("generation:" + strconv.FormatUint(generation, 10)))
	return mac.Sum(nil)
}

// Username is the configured admin account name.
func (sm *SessionManager) Username() string { return sm.username }

// TTL is the session lifetime.
func (sm *SessionManager) TTL() time.Duration { return sm.ttl }

// CheckCredentials compares username and password in constant time. Both
// sides are hashed first so the comparison does not leak their lengths, and
// both comparisons always run so it does not leak which one failed.
func (sm *SessionManager) CheckCredentials(username, password string) bool {
	u := sha256.Sum256([]byte(username))
	p := sha256.Sum256([]byte(password))
	okUser := subtle.ConstantTimeCompare(u[:], sm.userDigest[:])
	okPass := subtle.ConstantTimeCompare(p[:], sm.passDigest[:])
	return okUser&okPass == 1
}

// AllowLoginAttempt reserves a login attempt for the request's client (see
// loginLimiter.take and proxyConfig.clientKey).
func (sm *SessionManager) AllowLoginAttempt(r *http.Request) (bool, time.Duration) {
	return sm.limiter.take(sm.proxies.clientKey(r), peerKey(r))
}

// LoginSucceeded refunds the attempt reserved by AllowLoginAttempt.
func (sm *SessionManager) LoginSucceeded(r *http.Request) {
	sm.limiter.refund(sm.proxies.clientKey(r), peerKey(r))
}

// NewSession creates a session and sets its cookie on w.
func (sm *SessionManager) NewSession(w http.ResponseWriter, r *http.Request) Session {
	idBytes := make([]byte, 18)
	_, _ = rand.Read(idBytes)
	s := Session{
		ID:      base64.RawURLEncoding.EncodeToString(idBytes),
		Expires: sm.now().Add(sm.ttl).Truncate(time.Second),
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sm.sign(s),
		Path:     "/",
		Expires:  s.Expires,
		MaxAge:   int(sm.ttl / time.Second),
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	return s
}

// ClearCookie expires the session cookie in the browser.
func (sm *SessionManager) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// Revoke ends a session before its expiry: it is rejected from now on, any
// long-lived request bound to it (a WebSocket) is cancelled, and the
// revocation is persisted for restarts and other replicas. The local effect
// is immediate even if persisting fails; the error says the logout may not
// survive a restart or reach other replicas.
func (sm *SessionManager) Revoke(ctx context.Context, s Session) error {
	sm.mu.Lock()
	now := sm.now()
	for id, exp := range sm.revoked {
		if !now.Before(exp) {
			delete(sm.revoked, id)
		}
	}
	if len(sm.revoked) >= maxRevoked {
		// Everything still in the set would have to stay; instead end every
		// session at once by moving to a new signing key.
		log.Warn("UI login: session revocation list is full - rotating the signing key, all sessions on this process end")
		sm.generation++
		sm.key = sm.deriveKey(sm.generation)
		sm.revoked = make(map[string]time.Time)
		for id := range sm.watchers {
			sm.cancelWatchersLocked(id)
		}
	} else {
		sm.revoked[s.ID] = s.Expires
		sm.cancelWatchersLocked(s.ID)
	}
	sm.mu.Unlock()

	if sm.store == nil {
		return nil
	}
	return sm.store.Revoke(ctx, s.ID, s.Expires)
}

func (sm *SessionManager) cancelWatchersLocked(id string) {
	for _, cancel := range sm.watchers[id] {
		cancel()
	}
	delete(sm.watchers, id)
}

// Bind returns a context that is cancelled when s expires or is revoked, and
// a release func the caller must call when the request is done. This is what
// ends a WebSocket stream when its session does.
func (sm *SessionManager) Bind(ctx context.Context, s Session) (context.Context, func()) {
	ctx, cancel := context.WithDeadline(ctx, s.Expires)

	sm.mu.Lock()
	sm.nextWatch++
	n := sm.nextWatch
	if sm.watchers[s.ID] == nil {
		sm.watchers[s.ID] = make(map[uint64]context.CancelFunc)
	}
	sm.watchers[s.ID][n] = cancel
	sm.mu.Unlock()

	return ctx, func() {
		sm.mu.Lock()
		if w := sm.watchers[s.ID]; w != nil {
			delete(w, n)
			if len(w) == 0 {
				delete(sm.watchers, s.ID)
			}
		}
		sm.mu.Unlock()
		cancel()
	}
}

// SessionFromRequest returns the request's valid session, if any. Every
// session cookie is tried, not just the first: a sibling subdomain can plant
// a junk cookie of the same name (scoped to the parent domain) that browsers
// send ahead of ours, and that must not shadow the real session.
func (sm *SessionManager) SessionFromRequest(r *http.Request) (Session, bool) {
	for _, c := range r.Cookies() {
		if c.Name != SessionCookieName || c.Value == "" {
			continue
		}
		if s, ok := sm.verify(c.Value); ok {
			return s, true
		}
	}
	return Session{}, false
}

// Authenticate reports how r authenticates.
//
//   - A matching API key (X-API-Key or Authorization: Bearer) wins.
//   - A wrong X-API-Key is rejected outright: that header is ours, so a wrong
//     value is a misconfiguration that should fail loudly.
//   - A non-matching Authorization: Bearer is ignored when a valid session is
//     present - authenticating proxies forward their own bearer tokens - and
//     rejected when there is none.
func (sm *SessionManager) Authenticate(r *http.Request) Identity {
	if key := r.Header.Get(sm.headerName); key != "" {
		if sm.keyMatches(key) {
			return Identity{Method: MethodAPIKey}
		}
		return Identity{BadAPIKey: true}
	}
	bearer := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		bearer = strings.TrimPrefix(auth, "Bearer ")
	}
	if bearer != "" && sm.keyMatches(bearer) {
		return Identity{Method: MethodAPIKey}
	}
	if s, ok := sm.SessionFromRequest(r); ok {
		return Identity{Method: MethodSession, Session: &s}
	}
	if bearer != "" {
		return Identity{BadAPIKey: true}
	}
	return Identity{}
}

func (sm *SessionManager) keyMatches(key string) bool {
	return sm.apiKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(sm.apiKey)) == 1
}

// SameOrigin reports whether a cookie-authenticated request may be served:
// it came from this site's own pages, from an explicitly trusted CORS origin,
// or not from a browser at all. It is applied to every cookie-authenticated
// API request - reads included, since with CORS credentials allowed a
// foreign page could otherwise read responses - and is what stops cross-site
// request forgery and cross-site WebSocket hijacking on top of
// SameSite=Strict (which does not cover sibling subdomains: those are
// "same-site").
//
// It follows the algorithm of Go 1.25's http.CrossOriginProtection, which
// this module cannot use while it targets Go 1.24: trust Sec-Fetch-Site when
// the browser sends it, else compare Origin with Host (or X-Forwarded-Host),
// and allow requests with neither header - same-origin GETs from older
// browsers, top-level navigations, and non-browser clients. Only exact CORS
// origins are trusted; "*" and wildcard patterns such as
// "https://*.example.com" never are.
func (sm *SessionManager) SameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// Older browser, a WebSocket handshake (browsers send no Fetch
		// Metadata there), or not a browser: fall back to Origin.
	default: // "same-site", "cross-site"
		return sm.trustedOrigins[normalizeOrigin(origin)]
	}
	if origin == "" {
		return true
	}
	if sm.trustedOrigins[normalizeOrigin(origin)] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // includes "Origin: null"
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// A reverse proxy that rewrites Host usually reports the original in
	// X-Forwarded-Host. Trusting it here is safe: the forgery this check
	// stops comes from a victim's browser, and a cross-site page cannot add
	// headers to a WebSocket handshake or a form post.
	fwdHost, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	fwdHost = strings.TrimSpace(fwdHost)
	return fwdHost != "" && strings.EqualFold(u.Host, fwdHost)
}

func (sm *SessionManager) sign(s Session) string {
	payload := tokenVersion + "." + s.ID + "." + strconv.FormatInt(s.Expires.Unix(), 10)
	sm.mu.Lock()
	key := sm.key
	sm.mu.Unlock()
	return payload + "." + base64.RawURLEncoding.EncodeToString(macOf(key, payload))
}

var errNotSynced = errors.New("session revocations not loaded")

func (sm *SessionManager) verify(token string) (Session, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != tokenVersion || parts[1] == "" {
		return Session{}, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return Session{}, false
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2]

	sm.mu.Lock()
	key := sm.key
	_, revoked := sm.revoked[parts[1]]
	synced := sm.synced
	sm.mu.Unlock()

	if !hmac.Equal(sig, macOf(key, payload)) {
		return Session{}, false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Session{}, false
	}
	expires := time.Unix(exp, 0)
	if !sm.now().Before(expires) || revoked {
		return Session{}, false
	}
	if !synced {
		// Fail closed: without the revocation list a logged-out session
		// would be indistinguishable from a live one.
		log.WithError(errNotSynced).Debug("UI login: rejecting session")
		return Session{}, false
	}
	return Session{ID: parts[1], Expires: expires}, true
}

func macOf(key []byte, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// apiKeyFromRequest extracts the API key from the configured header or an
// "Authorization: Bearer" header, the same two forms Auth accepts.
func apiKeyFromRequest(r *http.Request, headerName string) string {
	if key := r.Header.Get(headerName); key != "" {
		return key
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// requestIsHTTPS decides the cookie's Secure flag. X-Forwarded-Proto is
// honoured without a trusted-proxy list because the only thing it can change
// is the Secure attribute of the cookie sent back to the same client that
// sent the header: a client spoofing it can only break or weaken its own
// session, never anyone else's.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
}
