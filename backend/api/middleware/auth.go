// Package middleware provides HTTP middleware for the InfraLens API.
package middleware

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// AuthConfig holds authentication configuration.
type AuthConfig struct {
	// APIKey is the shared secret for agent authentication.
	// If empty, authentication is disabled.
	APIKey string

	// HeaderName is the header to look for the API key (default: X-API-Key).
	HeaderName string

	// SkipPaths are exact request paths that don't require authentication.
	SkipPaths []string

	// SkipPrefixes are path prefixes that don't require authentication. Keep
	// this list short and specific: a prefix exempts everything beneath it,
	// including routes that don't exist yet.
	SkipPrefixes []string

	// AllowNoAuth permits running with no APIKey configured. Without it, an
	// empty APIKey is a startup error rather than a silently open server.
	AllowNoAuth bool

	// UIPassword enables UI login. When set, every route that is not
	// explicitly public requires a session cookie or the API key - see
	// UIAuth. When empty, the v3.0 rules above apply unchanged.
	UIPassword string

	// UIUsername is the single admin account's name (default "admin").
	UIUsername string

	// SessionSecret signs session cookies. Optional: when empty a random
	// per-process key is used, so sessions end on restart and are not
	// shared between replicas.
	SessionSecret string

	// SessionTTL is the absolute lifetime of a session (default 12h).
	SessionTTL time.Duration

	// TrustedProxies says whose X-Forwarded-For to believe when identifying
	// clients for login throttling: "" (default: loopback and private
	// ranges, one hop), "none", or a CIDR/IP list. See ratelimit.go.
	TrustedProxies string
}

// minUIPasswordLen is the shortest UI_PASSWORD accepted at all; below
// recommendedUIPasswordLen a warning is logged.
const (
	minUIPasswordLen         = 8
	recommendedUIPasswordLen = 12
)

// minSessionSecretLen is the shortest SESSION_SECRET accepted. It is an HMAC
// key, so anything guessable lets an attacker mint sessions offline.
const minSessionSecretLen = 32

// Validate reports whether the configuration is safe to serve with.
func (cfg AuthConfig) Validate() error {
	if cfg.APIKey == "" && !cfg.AllowNoAuth {
		return errors.New(
			"no API_KEY configured: event ingestion and AI endpoints would accept " +
				"unauthenticated requests. Set API_KEY, or set ALLOW_NO_AUTH=true to " +
				"run without authentication deliberately")
	}
	if cfg.SessionSecret != "" && len(cfg.SessionSecret) < minSessionSecretLen {
		return fmt.Errorf("SESSION_SECRET must be at least %d characters "+
			"(generate one with: openssl rand -hex 32)", minSessionSecretLen)
	}
	if cfg.SessionTTL < 0 {
		return errors.New("SESSION_TTL must be a positive duration")
	}
	if cfg.UIPassword != "" && len(cfg.UIPassword) < minUIPasswordLen {
		return fmt.Errorf("UI_PASSWORD must be at least %d characters "+
			"(generate one with: openssl rand -base64 24)", minUIPasswordLen)
	}
	if _, err := parseTrustedProxies(cfg.TrustedProxies); err != nil {
		return err
	}
	return nil
}

// DefaultAuthConfig returns the default auth configuration.
//
// These are matched exactly rather than by prefix. Prefix matching previously
// meant "/api/v1/topology" also exempted anything that might later be routed
// beneath it, so a new endpoint could silently lose authentication just by
// virtue of where it was mounted.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		HeaderName: "X-API-Key",
		SkipPaths: []string{
			"/health",
			"/ready",
			"/api/v1/ws",       // WebSocket has its own auth if needed
			"/api/v1/topology", // Read endpoints can be public
			"/api/v1/topology/export",
			"/api/v1/topology/history/range",
			"/api/v1/topology/history/stale",
			"/api/v1/topology/history/diff",
			"/api/v1/services",
			"/api/v1/graph/stats",
			"/api/v1/graph/criticality",
			"/api/v1/graph/orphans",
			"/api/v1/graph/impact",
			// The drift feed is topology history in another shape, already
			// pushed over the (public) WebSocket - same rule as the reads above.
			"/api/v1/drift/events",
			"/api/v1/drift/summary",
			"/api/v1/k8s/status",
			"/api/v1/ai/status",
			"/api/v1/ai/providers",
		},
		SkipPrefixes: []string{
			// Covers the per-service read endpoint /api/v1/services/{id}.
			// Service IDs contain literal "/" (e.g. "10.0.1.10/nginx"), so
			// this can't be enumerated as an exact path the way the entries
			// above are. /graph/impact takes the id as a query parameter
			// instead of a path segment for exactly this reason and needs no
			// prefix of its own - see router.go.
			//
			// Being a prefix, this exempts anything mounted under
			// /api/v1/services/ - including routes that don't exist yet. Only
			// add public reads there; anything privileged needs its own path.
			// The trailing slash matters: without it this would also exempt
			// e.g. /api/v1/services-admin.
			"/api/v1/services/",
		},
	}
}

// Auth creates an authentication middleware.
// If no API key is configured, the middleware is a no-op.
func Auth(cfg AuthConfig) func(http.Handler) http.Handler {
	if cfg.APIKey == "" {
		log.Warn("API key authentication is disabled - no API_KEY configured")
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	if cfg.HeaderName == "" {
		cfg.HeaderName = "X-API-Key"
	}

	skipSet := make(map[string]bool)
	for _, p := range cfg.SkipPaths {
		skipSet[p] = true
	}

	log.WithField("header", cfg.HeaderName).Info("API key authentication enabled")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The skip-list decision is made on the cleaned path, not the raw
			// one. net/url.Parse does not collapse "." / ".." segments, so a
			// request for "/api/v1/services/../events" arrives with that
			// literal path; naive prefix matching against it would exempt a
			// protected route it merely starts with the text of. gorilla/mux
			// happens to intercept dirty paths on its own and redirect before
			// any handler runs, which is what actually prevents this today -
			// but that is mux's default, not a guarantee this middleware
			// controls, and it would silently stop applying if the router
			// ever called SkipClean(true) for an unrelated reason. Cleaning
			// here makes the auth decision correct on its own.
			path := stdpath.Clean(r.URL.Path)
			if skipSet[path] {
				next.ServeHTTP(w, r)
				return
			}

			// Explicitly declared prefixes only (e.g. /api/v1/services/{id})
			skipped := false
			for _, prefix := range cfg.SkipPrefixes {
				if strings.HasPrefix(path, prefix) {
					skipped = true
					break
				}
			}
			if skipped {
				next.ServeHTTP(w, r)
				return
			}

			// Get API key from header
			providedKey := r.Header.Get(cfg.HeaderName)
			if providedKey == "" {
				// Also check Authorization: Bearer <key>
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					providedKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			if providedKey == "" {
				log.WithFields(log.Fields{
					"path":   path,
					"method": r.Method,
					"remote": r.RemoteAddr,
				}).Warn("Missing API key")
				http.Error(w, "Unauthorized: API key required", http.StatusUnauthorized)
				return
			}

			// Constant-time comparison to prevent timing attacks
			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(cfg.APIKey)) != 1 {
				log.WithFields(log.Fields{
					"path":   path,
					"method": r.Method,
					"remote": r.RemoteAddr,
				}).Warn("Invalid API key")
				http.Error(w, "Unauthorized: Invalid API key", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth is a helper that returns 401 if not authenticated.
// Use this for individual handlers that need auth even if middleware is disabled.
func RequireAuth(apiKey string, next http.HandlerFunc) http.HandlerFunc {
	if apiKey == "" {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				providedKey = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	}
}
