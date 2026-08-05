// Package config provides configuration management for InfraLens.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/storage"
)

// Config holds all configuration for the InfraLens backend.
type Config struct {
	// Server configuration
	Server ServerConfig

	// Storage configuration
	Storage storage.Config

	// CORS configuration
	CORS middleware.CORSConfig

	// Auth configuration
	Auth middleware.AuthConfig

	// LLM configuration
	LLM LLMConfig
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	ListenAddr   string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	Debug        bool

	// DemoMode enables the built-in topology simulator so the UI can be
	// explored without any agents (no Linux/eBPF required).
	DemoMode bool

	// MaxRequestBytes caps the size of any request body. Agents post batched
	// events, so this needs headroom, but unbounded decoding lets a single
	// request exhaust memory.
	MaxRequestBytes int64
}

// LLMConfig holds LLM provider configuration.
type LLMConfig struct {
	OpenAIAPIKey    string
	OpenAIModel     string
	AnthropicAPIKey string
	AnthropicModel  string
	GeminiAPIKey    string
	GeminiModel     string
	OllamaURL       string
	OllamaModel     string
	LMStudioURL     string
	LMStudioModel   string
	DefaultProvider string
}

// Load loads configuration from environment variables.
func Load() *Config {
	cfg := &Config{
		Server: ServerConfig{
			ListenAddr:      getEnv("LISTEN_ADDR", ":8080"),
			ReadTimeout:     getDurationEnv("READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    getDurationEnv("WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     getDurationEnv("IDLE_TIMEOUT", 60*time.Second),
			Debug:           getBoolEnv("DEBUG", false),
			DemoMode:        getBoolEnv("DEMO_MODE", false),
			MaxRequestBytes: int64(getIntEnv("MAX_REQUEST_BYTES", 10<<20)), // 10 MiB
		},

		Storage: storage.Config{
			Driver:          getEnv("DB_DRIVER", "sqlite"),
			DSN:             getEnv("DB_DSN", "infralens.db"),
			MaxOpenConns:    getIntEnv("DB_MAX_OPEN_CONNS", 1),
			MaxIdleConns:    getIntEnv("DB_MAX_IDLE_CONNS", 1),
			ConnMaxLifetime: getDurationEnv("DB_CONN_MAX_LIFETIME", 0),
			AutoMigrate:     getBoolEnv("DB_AUTO_MIGRATE", true),
			PruneInterval:   getDurationEnv("PRUNE_INTERVAL", 5*time.Minute),
			PruneMaxAge:     getDurationEnv("PRUNE_MAX_AGE", 30*time.Minute),
		},

		CORS: middleware.CORSConfig{
			AllowedOrigins: middleware.ParseCORSOrigins(getEnv("CORS_ORIGINS", "*")),
			AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"Content-Type", "Authorization", "X-API-Key"},
			// Defaults to false: with the default wildcard origin, browsers
			// reject "Access-Control-Allow-Origin: *" combined with
			// "Access-Control-Allow-Credentials: true" outright, so defaulting
			// it on only produced silently failing cross-origin requests.
			AllowCredentials: getBoolEnv("CORS_CREDENTIALS", false),
			MaxAge:           getIntEnv("CORS_MAX_AGE", 3600),
		},

		Auth: middleware.AuthConfig{
			APIKey:       getEnv("API_KEY", ""),
			HeaderName:   getEnv("API_KEY_HEADER", "X-API-Key"),
			SkipPaths:    middleware.DefaultAuthConfig().SkipPaths,
			SkipPrefixes: middleware.DefaultAuthConfig().SkipPrefixes,
			// Running with no API key leaves every ingest and AI endpoint open.
			// That has to be a deliberate choice, not something you get by
			// forgetting to set a variable.
			AllowNoAuth: getBoolEnv("ALLOW_NO_AUTH", false),
		},

		LLM: LLMConfig{
			OpenAIAPIKey:    getEnv("OPENAI_API_KEY", ""),
			OpenAIModel:     getEnv("OPENAI_MODEL", ""),
			AnthropicAPIKey: getEnv("ANTHROPIC_API_KEY", ""),
			AnthropicModel:  getEnv("ANTHROPIC_MODEL", ""),
			GeminiAPIKey:    getEnv("GEMINI_API_KEY", ""),
			GeminiModel:     getEnv("GEMINI_MODEL", ""),
			OllamaURL:       getEnv("OLLAMA_URL", ""),
			OllamaModel:     getEnv("OLLAMA_MODEL", ""),
			LMStudioURL:     getEnv("LMSTUDIO_URL", ""),
			LMStudioModel:   getEnv("LMSTUDIO_MODEL", ""),
			DefaultProvider: getEnv("DEFAULT_LLM_PROVIDER", ""),
		},
	}

	return cfg
}

// Helper functions

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getIntEnv(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getBoolEnv(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		return strings.ToLower(val) == "true" || val == "1"
	}
	return defaultVal
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}
