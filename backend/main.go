// Package main implements the InfraLens backend aggregator.
// This server receives traffic data from agents running on each node
// and provides an API for the frontend to visualize the service topology.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/Herenn/Infralens/backend/api"
	"github.com/Herenn/Infralens/backend/graph"
	"github.com/Herenn/Infralens/backend/k8s"
	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
)

var (
	listenAddr = flag.String("listen", ":8080", "HTTP listen address")
	debug      = flag.Bool("debug", false, "Enable debug logging")
)

func main() {
	flag.Parse()

	// Configure logging
	if *debug {
		log.SetLevel(log.DebugLevel)
	}
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	log.WithField("addr", *listenAddr).Info("Starting InfraLens backend")

	// Initialize Kubernetes watcher for service discovery
	k8sWatcher := k8s.NewWatcher()

	// Start Kubernetes watcher in background
	go k8sWatcher.Start()

	// Initialize the service graph
	serviceGraph := graph.NewServiceGraph()

	// Create API handler with Kubernetes watcher for IP resolution
	apiHandler := api.NewHandler(serviceGraph, k8sWatcher)

	// Initialize LLM manager from environment variables
	llmConfig := &llm.Config{
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:     os.Getenv("OPENAI_MODEL"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  os.Getenv("ANTHROPIC_MODEL"),
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		GeminiModel:     os.Getenv("GEMINI_MODEL"),
		OllamaURL:       os.Getenv("OLLAMA_URL"),
		OllamaModel:     os.Getenv("OLLAMA_MODEL"),
		LMStudioURL:     os.Getenv("LMSTUDIO_URL"),
		LMStudioModel:   os.Getenv("LMSTUDIO_MODEL"),
		DefaultProvider: llm.Provider(os.Getenv("DEFAULT_LLM_PROVIDER")),
	}
	llmManager := llm.NewManager(llmConfig)
	apiHandler.SetLLMManager(llmManager)
	
	// Log configured providers
	for provider, enabled := range llmManager.Status() {
		if enabled {
			log.WithField("provider", provider).Info("LLM provider configured")
		}
	}

	// Setup router
	router := mux.NewRouter()
	apiHandler.RegisterRoutes(router)

	// Setup CORS for frontend
	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	})

	// Create HTTP server
	srv := &http.Server{
		Addr:         *listenAddr,
		Handler:      c.Handler(router),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in goroutine
	go func() {
		log.WithField("addr", *listenAddr).Info("HTTP server started")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Fatal("HTTP server failed")
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.WithField("signal", sig).Info("Received shutdown signal")

	// Stop Kubernetes watcher
	k8sWatcher.Stop()

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Error("HTTP server shutdown failed")
	}

	log.Info("Backend shutdown complete")
}
