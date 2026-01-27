// Package middleware provides HTTP middleware for the InfraLens API.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// AuthConfig holds authentication configuration.
type AuthConfig struct {
	// APIKey is the shared secret for agent authentication.
	// If empty, authentication is disabled.
	APIKey string

	// HeaderName is the header to look for the API key (default: X-API-Key).
	HeaderName string

	// SkipPaths are paths that don't require authentication.
	SkipPaths []string
}

// DefaultAuthConfig returns the default auth configuration.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		HeaderName: "X-API-Key",
		SkipPaths: []string{
			"/health",
			"/ready",
			"/api/v1/ws",           // WebSocket has its own auth if needed
			"/api/v1/topology",      // Read endpoints can be public
			"/api/v1/services",
			"/api/v1/graph/stats",
			"/api/v1/k8s/status",
			"/api/v1/ai/status",
			"/api/v1/ai/providers",
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
			// Check if path should skip auth
			path := r.URL.Path
			if skipSet[path] {
				next.ServeHTTP(w, r)
				return
			}

			// Also check prefix matches for paths like /api/v1/services/{id}
			for skipPath := range skipSet {
				if strings.HasPrefix(path, skipPath) {
					next.ServeHTTP(w, r)
					return
				}
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
