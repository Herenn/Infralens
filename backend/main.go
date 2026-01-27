// Package main implements the InfraLens backend aggregator.
// This server receives traffic data from agents running on each node
// and provides an API for the frontend to visualize the service topology.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Herenn/Infralens/backend/api"
	"github.com/Herenn/Infralens/backend/config"
	"github.com/Herenn/Infralens/backend/k8s"
	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/postgres"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
	log "github.com/sirupsen/logrus"
)

var (
	listenAddr = flag.String("listen", "", "HTTP listen address (overrides LISTEN_ADDR env)")
	debug      = flag.Bool("debug", false, "Enable debug logging (overrides DEBUG env)")
)

func main() {
	flag.Parse()

	// Load configuration from environment
	cfg := config.Load()

	// Override from flags if provided
	if *listenAddr != "" {
		cfg.Server.ListenAddr = *listenAddr
	}
	if *debug {
		cfg.Server.Debug = true
	}

	// Configure logging
	if cfg.Server.Debug {
		log.SetLevel(log.DebugLevel)
	}
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	log.WithFields(log.Fields{
		"addr":    cfg.Server.ListenAddr,
		"debug":   cfg.Server.Debug,
		"db":      cfg.Storage.DSN,
		"driver":  cfg.Storage.Driver,
		"version": "1.0.0",
	}).Info("Starting InfraLens backend")

	// Initialize storage based on driver
	store, err := initStorage(cfg.Storage)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize storage")
	}
	defer store.Close()

	log.WithField("driver", cfg.Storage.Driver).Info("Storage initialized successfully")

	// Initialize Kubernetes watcher for service discovery
	k8sWatcher := k8s.NewWatcher()
	go k8sWatcher.Start()
	defer k8sWatcher.Stop()

	// Initialize LLM manager
	llmConfig := &llm.Config{
		OpenAIAPIKey:    cfg.LLM.OpenAIAPIKey,
		OpenAIModel:     cfg.LLM.OpenAIModel,
		AnthropicAPIKey: cfg.LLM.AnthropicAPIKey,
		AnthropicModel:  cfg.LLM.AnthropicModel,
		GeminiAPIKey:    cfg.LLM.GeminiAPIKey,
		GeminiModel:     cfg.LLM.GeminiModel,
		OllamaURL:       cfg.LLM.OllamaURL,
		OllamaModel:     cfg.LLM.OllamaModel,
		LMStudioURL:     cfg.LLM.LMStudioURL,
		LMStudioModel:   cfg.LLM.LMStudioModel,
		DefaultProvider: llm.Provider(cfg.LLM.DefaultProvider),
	}
	llmManager := llm.NewManager(llmConfig)

	// Log configured providers
	for provider, enabled := range llmManager.Status() {
		if enabled {
			log.WithField("provider", provider).Info("LLM provider configured")
		}
	}

	// Create API server
	apiServer := api.NewServer(cfg, store, k8sWatcher, llmManager)
	defer apiServer.Close()

	// Create HTTP server
	srv := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      apiServer.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start server in goroutine
	go func() {
		log.WithField("addr", cfg.Server.ListenAddr).Info("HTTP server started")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Fatal("HTTP server failed")
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.WithField("signal", sig).Info("Received shutdown signal")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Error("HTTP server shutdown failed")
	}

	log.Info("Backend shutdown complete")
}

// initStorage initializes the appropriate storage backend based on configuration.
func initStorage(cfg storage.Config) (storage.Store, error) {
	switch cfg.Driver {
	case "postgres", "postgresql":
		// Adjust defaults for PostgreSQL
		if cfg.MaxOpenConns == 1 {
			cfg.MaxOpenConns = 25
		}
		if cfg.MaxIdleConns == 1 {
			cfg.MaxIdleConns = 5
		}
		return postgres.New(cfg)

	case "sqlite", "sqlite3", "":
		return sqlite.New(cfg)

	default:
		return nil, fmt.Errorf("unsupported database driver: %s (supported: sqlite, postgres)", cfg.Driver)
	}
}
