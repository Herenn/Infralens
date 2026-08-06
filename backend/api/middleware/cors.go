package middleware

import (
	"net/http"
	"strings"

	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
)

// CORSConfig holds CORS configuration.
type CORSConfig struct {
	// AllowedOrigins is a list of origins allowed to make cross-origin requests.
	// Use "*" to allow all origins (not recommended for production).
	AllowedOrigins []string

	// AllowedMethods is a list of methods allowed.
	AllowedMethods []string

	// AllowedHeaders is a list of headers allowed.
	AllowedHeaders []string

	// AllowCredentials indicates whether credentials (cookies, auth headers) are allowed.
	AllowCredentials bool

	// MaxAge is the max time (in seconds) a preflight request can be cached.
	MaxAge int
}

// DefaultCORSConfig returns sensible CORS defaults.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-API-Key"},
		AllowCredentials: true,
		MaxAge:           3600,
	}
}

// ParseCORSOrigins parses a comma-separated list of origins.
func ParseCORSOrigins(origins string) []string {
	if origins == "" {
		return []string{"*"}
	}

	parts := strings.Split(origins, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}

	if len(result) == 0 {
		return []string{"*"}
	}

	return result
}

// CORS creates a CORS middleware handler.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	wildcard := len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*"

	// A wildcard origin and credentials are mutually exclusive per the CORS
	// spec: browsers reject "Access-Control-Allow-Origin: *" alongside
	// "Access-Control-Allow-Credentials: true", so emitting both just makes
	// every credentialed cross-origin request fail. Drop credentials rather
	// than ship a combination that cannot work.
	if wildcard && cfg.AllowCredentials {
		log.Warn("CORS: ignoring AllowCredentials because all origins (*) are allowed - " +
			"browsers reject that combination. Set CORS_ORIGINS to explicit origins to use credentials.")
		cfg.AllowCredentials = false
	}

	// Log configuration
	if wildcard {
		log.Warn("CORS is allowing all origins (*) - not recommended for production")
	} else {
		log.WithField("origins", cfg.AllowedOrigins).Info("CORS configured with allowed origins")
	}

	c := cors.New(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   cfg.AllowedMethods,
		AllowedHeaders:   cfg.AllowedHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           cfg.MaxAge,
	})

	return c.Handler
}

// CORSHandler returns a configured cors.Cors instance.
// Use this if you need the underlying cors.Cors object.
func CORSHandler(cfg CORSConfig) *cors.Cors {
	return cors.New(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   cfg.AllowedMethods,
		AllowedHeaders:   cfg.AllowedHeaders,
		AllowCredentials: cfg.AllowCredentials,
		MaxAge:           cfg.MaxAge,
	})
}
