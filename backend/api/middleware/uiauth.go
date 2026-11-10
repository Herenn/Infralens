package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	stdpath "path"

	log "github.com/sirupsen/logrus"
)

// Paths that are public in every mode. Matched exactly against an already
// clean request path - see isCleanPath.
var alwaysPublicPaths = map[string]bool{
	"/health":             true,
	"/ready":              true,
	"/api/v1/auth/login":  true,
	"/api/v1/auth/logout": true,
	"/api/v1/auth/status": true,
}

// selfAuthenticatingPaths are endpoints that do their own authentication and
// so bypass this middleware entirely - session and API key checks alike, in
// both modes. Exact, clean paths only.
//
// CONTRACT: a handler listed here MUST enforce its own authentication on
// every request. Listing it removes all protection this middleware gives,
// including the same-origin check. Do not list anything that accepts the UI
// session cookie.
//
// Empty by default. The MCP server will be added here as "/mcp" when it is
// mounted (it authenticates with MCP_API_KEY or API_KEY, never cookies).
var selfAuthenticatingPaths = map[string]bool{}

// Machine endpoints keep v3.0 semantics when UI login is on: the API key and
// nothing else (agents cannot hold a session, and a browser session has no
// business posting topology), open only if the operator chose ALLOW_NO_AUTH.
var apiKeyOnlyPaths = map[string]bool{
	"/api/v1/events":     true,
	"/api/v1/stats":      true,
	"/api/v1/metrics":    true,
	"/api/v1/inspection": true,
	"/metrics":           true, // Prometheus scrape
}

// UIAuth is the API's authentication middleware.
//
// With sm == nil (UI_PASSWORD unset) it behaves exactly like v3.0's Auth -
// declared read endpoints public, everything else behind the API key when
// one is set - except that /api/v1/auth/* is reachable so the UI can learn
// that no login is needed.
//
// With a SessionManager it is deny-by-default: every route requires a valid
// session cookie or the API key, except the short alwaysPublicPaths list,
// the agent endpoints in apiKeyOnlyPaths, and selfAuthenticatingPaths.
// Routes added later are therefore protected without anyone having to
// register them. Every cookie-authenticated request - reads included - must
// also pass the same-origin check (SameOrigin), and is bound to the
// session's lifetime (Bind) so a WebSocket closes when its session expires or
// is logged out.
func UIAuth(cfg AuthConfig, sm *SessionManager) func(http.Handler) http.Handler {
	if sm == nil {
		log.Warn("UI login is disabled (UI_PASSWORD not set) - topology, history, services, graph " +
			"and WebSocket endpoints are readable without authentication. Set UI_PASSWORD to require login.")
		legacy := Auth(cfg)
		return func(next http.Handler) http.Handler {
			protected := legacy(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isCleanPath(r.URL.Path) && (alwaysPublicPaths[r.URL.Path] || selfAuthenticatingPaths[r.URL.Path]) {
					next.ServeHTTP(w, r)
					return
				}
				protected.ServeHTTP(w, r)
			})
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			// Exemptions apply only to paths that are already clean. A dirty
			// path ("/health/../api/v1/topology", "/api/v1/services/x/../../../health")
			// gets no exemption at all, whatever it would clean to: the
			// router redirects such requests rather than serving them, but
			// if it ever served the raw path (SkipClean, or a greedy route
			// like /services/{id:.+}), the auth decision must not have been
			// made on a different path than the one routed.
			if isCleanPath(path) {
				if alwaysPublicPaths[path] || selfAuthenticatingPaths[path] {
					next.ServeHTTP(w, r)
					return
				}
				if apiKeyOnlyPaths[path] {
					serveAPIKeyOnly(cfg, sm, next, w, r)
					return
				}
			}

			id := sm.Authenticate(r)
			switch id.Method {
			case MethodAPIKey:
				next.ServeHTTP(w, r)
				return

			case MethodSession:
				if !sm.SameOrigin(r) {
					log.WithFields(log.Fields{
						"path":   path,
						"method": r.Method,
						"origin": r.Header.Get("Origin"),
						"remote": r.RemoteAddr,
					}).Warn("Rejected cross-origin request made with a session cookie")
					writeAuthError(w, http.StatusForbidden, "cross-origin request rejected")
					return
				}
				ctx, release := sm.Bind(r.Context(), *id.Session)
				defer release()
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			fields := log.Fields{"path": path, "method": r.Method, "remote": r.RemoteAddr}
			if id.BadAPIKey {
				log.WithFields(fields).Warn("Invalid API key")
				writeAuthError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			log.WithFields(fields).Debug("Unauthenticated request rejected")
			writeAuthError(w, http.StatusUnauthorized, "authentication required")
		})
	}
}

func serveAPIKeyOnly(cfg AuthConfig, sm *SessionManager, next http.Handler, w http.ResponseWriter, r *http.Request) {
	if cfg.APIKey == "" {
		// ALLOW_NO_AUTH: open, exactly as in v3.0.
		next.ServeHTTP(w, r)
		return
	}
	key := apiKeyFromRequest(r, sm.headerName)
	if key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(cfg.APIKey)) != 1 {
		log.WithFields(log.Fields{
			"path":   r.URL.Path,
			"method": r.Method,
			"remote": r.RemoteAddr,
		}).Warn("Missing or invalid API key")
		writeAuthError(w, http.StatusUnauthorized, "API key required")
		return
	}
	next.ServeHTTP(w, r)
}

// isCleanPath reports whether p is already in canonical form, so that an
// exact-match exemption on it means what it says.
func isCleanPath(p string) bool {
	return p != "" && stdpath.Clean(p) == p
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
