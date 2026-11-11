// Package config provides configuration management for InfraLens.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
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

	// Architecture drift notifications
	Drift DriftConfig

	// Idle spend estimation (see idlecost.go)
	IdleCost service.IdleCostConfig

	// MCP (Model Context Protocol) endpoint for AI assistants
	MCP MCPConfig
}

// MCPConfig configures the read-only MCP endpoint served at /mcp.
type MCPConfig struct {
	// Enabled mounts /mcp. On by default: it is read-only, and what it serves
	// is the same topology the public read endpoints (/api/v1/topology,
	// /services, /graph/*, /topology/history/*, /topology/export) serve. The
	// one addition - a service's process fingerprint (process name, listen
	// ports, DB/HTTP server banners, pod) - is only returned to callers that
	// authenticated with a key, never in open mode.
	Enabled bool

	// APIKey (MCP_API_KEY) is a read-only key valid on /mcp only, so AI
	// assistant configs never need the agents' write-capable API_KEY. /mcp
	// accepts either key; any key configured makes one required.
	APIKey string

	// AllowedOrigins are browser origins allowed to call /mcp (Origin check
	// and CORS) besides loopback ones. Non-browser clients send no Origin and
	// are unaffected.
	AllowedOrigins []string
}

// DriftConfig configures architecture drift detection: a dependency appears
// that has never been seen before, or an established one stops being
// observed. Detection runs whenever history is enabled and records changes in
// the in-UI feed; the webhook is optional, off unless WebhookURL is set.
type DriftConfig struct {
	WebhookURL string
	Interval   time.Duration
	GoneAfter  time.Duration
	Warmup     time.Duration

	// DetectionEnabled switches drift detection (feed and webhook) off
	// entirely when false. Default true.
	DetectionEnabled bool
}

// Enabled reports whether drift webhook notifications are configured.
func (c DriftConfig) Enabled() bool {
	return c.WebhookURL != ""
}

// Validate rejects a webhook URL that could never be delivered to, so a typo
// fails at startup instead of silently dropping every notification. The URL
// itself is never included in the error: Slack-style webhook URLs embed their
// secret.
func (c DriftConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	u, err := url.Parse(c.WebhookURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("DRIFT_WEBHOOK_URL must be an absolute http(s) URL")
	}
	return nil
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

			// Topology history. Retention is deliberately unrelated to
			// PRUNE_MAX_AGE above: that governs current state and is measured
			// in minutes, while history is what makes "show me last week"
			// possible and is measured in days.
			HistoryEnabled:   getBoolEnv("HISTORY_ENABLED", true),
			HistoryRetention: getDurationEnv("HISTORY_RETENTION", storage.DefaultHistoryRetention),
			HistoryMaxGap:    getDurationEnv("HISTORY_MAX_GAP", storage.DefaultHistoryMaxGap),
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
			// UI login (opt-in). Unset UI_PASSWORD keeps v3.0 behaviour.
			UIPassword:    getEnv("UI_PASSWORD", ""),
			UIUsername:    getEnv("UI_USERNAME", middleware.DefaultUIUsername),
			SessionSecret: getEnv("SESSION_SECRET", ""),
			SessionTTL:    getDurationEnv("SESSION_TTL", middleware.DefaultSessionTTL),
			// Whose X-Forwarded-For to believe for login throttling.
			TrustedProxies: getEnv("TRUSTED_PROXIES", ""),
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

		// Unset durations fall back to the service package's defaults (5m
		// check interval, 24h gone-after, 1h warmup) - see DriftConfig there.
		// Warmup's unset sentinel is -1 rather than 0 so DRIFT_WARMUP=0s can
		// mean "no warmup".
		Drift: DriftConfig{
			WebhookURL: getEnv("DRIFT_WEBHOOK_URL", ""),
			Interval:   getDurationEnv("DRIFT_CHECK_INTERVAL", 0),
			GoneAfter:  getDurationEnv("DRIFT_GONE_AFTER", 0),
			Warmup:     getDurationEnv("DRIFT_WARMUP", -1),

			DetectionEnabled: getBoolEnv("DRIFT_ENABLED", true),
		},

		IdleCost: loadIdleCost(),

		MCP: MCPConfig{
			Enabled:        getBoolEnv("MCP_ENABLED", true),
			APIKey:         getEnv("MCP_API_KEY", ""),
			AllowedOrigins: getListEnv("MCP_ALLOWED_ORIGINS"),
		},
	}

	// The demo writes two weeks of synthetic history and drift events. With
	// the default SQLite file shared, trying the demo and then running for
	// real would leave that fake past in the real database for the whole
	// retention window - so the demo gets its own file unless told otherwise.
	if cfg.Server.DemoMode && os.Getenv("DB_DSN") == "" && isSQLiteDriver(cfg.Storage.Driver) {
		cfg.Storage.DSN = DefaultDemoDSN
	}

	if gap := cfg.Storage.HistoryMaxGap; gap > 0 && gap < MinHistoryMaxGap {
		log.WithFields(log.Fields{"HISTORY_MAX_GAP": gap, "using": MinHistoryMaxGap}).Warn(
			"HISTORY_MAX_GAP is shorter than a few agent heartbeats (host metrics every 5s): every heartbeat " +
				"would open a new node interval and observation coverage would fragment; raising it")
		cfg.Storage.HistoryMaxGap = MinHistoryMaxGap
	}

	return cfg
}

// MinHistoryMaxGap is the shortest HISTORY_MAX_GAP accepted: three default
// agent heartbeat periods (host metrics every 5s). History refreshes are
// throttled to half the gap, so below this consecutive heartbeats no longer
// extend one interval and node coverage (see service/coverage.go) breaks
// into a row per heartbeat.
const MinHistoryMaxGap = 15 * time.Second

// DefaultDemoDSN is the SQLite database demo mode uses when DB_DSN is unset.
const DefaultDemoDSN = "infralens-demo.db"

func isSQLiteDriver(driver string) bool {
	return driver == "" || driver == "sqlite" || driver == "sqlite3"
}

// getListEnv reads a comma-separated list, dropping empty entries.
func getListEnv(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
