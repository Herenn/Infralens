// Package api provides the HTTP API for InfraLens.
package api

import (
	"context"
	"net/http"
	"runtime"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/config"
	"github.com/Herenn/Infralens/backend/k8s"
	"github.com/Herenn/Infralens/backend/pkg/llm"
	"github.com/Herenn/Infralens/backend/pkg/metrics"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// Server represents the API server.
type Server struct {
	router     *mux.Router
	config     *config.Config
	store      storage.Store
	topology   *service.TopologyService
	processor  *service.EventProcessor
	eventBus   *service.EventBus
	k8sWatcher *k8s.Watcher
	llmManager *llm.Manager

	// stopBackground cancels goroutines the server owns (drift detection).
	stopBackground context.CancelFunc
}

// NewServer creates a new API server.
func NewServer(
	cfg *config.Config,
	store storage.Store,
	k8sWatcher *k8s.Watcher,
	llmManager *llm.Manager,
) *Server {
	// Create event bus for real-time updates
	eventBus := service.NewEventBus(100)

	// Create topology service
	topologySvc := service.NewTopologyService(store, eventBus)
	if cfg.Storage.HistoryEnabled {
		topologySvc.EnableHistory(cfg.Storage.HistoryMaxGap, cfg.Storage.HistoryRetention)
	}

	// Create event processor
	processor := service.NewEventProcessor(topologySvc, k8sWatcher)

	s := &Server{
		router:     mux.NewRouter(),
		config:     cfg,
		store:      store,
		topology:   topologySvc,
		processor:  processor,
		eventBus:   eventBus,
		k8sWatcher: k8sWatcher,
		llmManager: llmManager,
	}

	// Set build info for metrics
	metrics.SetBuildInfo(handlers.Version, runtime.Version())

	bgCtx, cancel := context.WithCancel(context.Background())
	s.stopBackground = cancel
	s.startDriftDetection(bgCtx)

	s.setupRoutes()
	return s
}

// startDriftDetection starts architecture drift notifications if a webhook is
// configured. Drift is computed from topology history, so it needs history on.
func (s *Server) startDriftDetection(ctx context.Context) {
	if !s.config.Drift.Enabled() {
		return
	}
	detector, err := s.topology.NewDriftDetector(service.DriftConfig{
		Interval:  s.config.Drift.Interval,
		GoneAfter: s.config.Drift.GoneAfter,
		Warmup:    s.config.Drift.Warmup,
	}, service.NewWebhookDriftNotifier(s.config.Drift.WebhookURL))
	if err != nil {
		log.WithError(err).Warn("DRIFT_WEBHOOK_URL is set but drift detection could not start " +
			"(it requires HISTORY_ENABLED=true) - no drift notifications will be sent")
		return
	}
	log.Info("Architecture drift notifications enabled")
	go detector.Run(ctx)
}

// setupRoutes configures all API routes.
func (s *Server) setupRoutes() {
	// Create handlers
	eventHandler := handlers.NewEventHandler(s.processor)
	topologyHandler := handlers.NewTopologyHandler(s.topology)
	exportHandler := handlers.NewExportHandler(s.topology)
	wsHandler := handlers.NewWebSocketHandler(s.topology, s.eventBus)
	healthHandler := handlers.NewHealthHandler(s.store, s.k8sWatcher)
	aiHandler := handlers.NewAIHandler(s.topology, s.llmManager)

	// API v1 subrouter
	api := s.router.PathPrefix("/api/v1").Subrouter()

	// Event ingestion endpoints (require auth)
	api.HandleFunc("/events", eventHandler.HandleEvents).Methods("POST")
	api.HandleFunc("/stats", eventHandler.HandleStats).Methods("POST")
	api.HandleFunc("/metrics", eventHandler.HandleMetrics).Methods("POST")
	api.HandleFunc("/inspection", eventHandler.HandleInspection).Methods("POST")

	// Topology query endpoints
	api.HandleFunc("/topology", topologyHandler.HandleGetTopology).Methods("GET")
	api.HandleFunc("/topology/export", exportHandler.HandleExport).Methods("GET")
	api.HandleFunc("/topology/history/range", topologyHandler.HandleGetHistoryRange).Methods("GET")
	api.HandleFunc("/topology/history/stale", topologyHandler.HandleGetStaleServices).Methods("GET")
	api.HandleFunc("/topology/history/diff", topologyHandler.HandleGetTopologyDiff).Methods("GET")
	api.HandleFunc("/services", topologyHandler.HandleGetServices).Methods("GET")
	// Service IDs routinely contain a literal "/" (e.g. "10.0.1.10/nginx"),
	// so {id} must be greedy (".+", not the default "[^/]+") to match them at
	// all. Impact is deliberately NOT a nested path segment
	// ("/services/{id}/impact"): a service whose ID happens to end in the
	// literal substring "/impact" (e.g. a real service named "impact" - not
	// far-fetched) would be indistinguishable at the URL level from the
	// bare ID ".../impact" plus an /impact suffix, and no amount of route
	// ordering fixes that ambiguity since both parse to the same path. A
	// query parameter carries the id outside the path entirely, matching
	// direction/depth/at which are already query params on this endpoint.
	api.HandleFunc("/graph/impact", topologyHandler.HandleGetImpact).Methods("GET")
	api.HandleFunc("/services/{id:.+}", topologyHandler.HandleGetService).Methods("GET")
	api.HandleFunc("/graph/stats", topologyHandler.HandleGetStats).Methods("GET")
	api.HandleFunc("/graph/criticality", topologyHandler.HandleGetCriticality).Methods("GET")
	api.HandleFunc("/graph/orphans", topologyHandler.HandleGetOrphans).Methods("GET")

	// WebSocket endpoint
	api.HandleFunc("/ws", wsHandler.HandleWebSocket)

	// AI documentation endpoints
	api.HandleFunc("/ai/status", aiHandler.HandleStatus).Methods("GET")
	api.HandleFunc("/ai/config", aiHandler.HandleConfig).Methods("GET", "POST")
	api.HandleFunc("/ai/docs", aiHandler.HandleDocs).Methods("POST")
	api.HandleFunc("/ai/ask", aiHandler.HandleAsk).Methods("POST")
	api.HandleFunc("/ai/providers", aiHandler.HandleProviders).Methods("GET")

	// Health/Ready endpoints
	s.router.HandleFunc("/health", healthHandler.HandleHealth).Methods("GET")
	s.router.HandleFunc("/ready", healthHandler.HandleReady).Methods("GET")

	// K8s status
	api.HandleFunc("/k8s/status", healthHandler.HandleK8sStatus).Methods("GET")

	// Version endpoint
	api.HandleFunc("/version", healthHandler.HandleVersion).Methods("GET")

	// Prometheus metrics endpoint
	s.router.Handle("/metrics", promhttp.Handler()).Methods("GET")
}

// Handler returns the HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	// Apply middleware in order: Recovery -> Metrics -> Logging -> BodyLimit -> CORS -> Auth
	// Note: Metrics is before Logging so we don't double-count
	handler := middleware.Chain(
		middleware.Recovery(),
		middleware.Metrics(),
		middleware.Logging(),
		middleware.BodyLimit(s.config.Server.MaxRequestBytes),
		middleware.CORS(s.config.CORS),
		middleware.Auth(s.config.Auth),
	)(s.router)

	return handler
}

// EventBus returns the event bus for external access.
func (s *Server) EventBus() *service.EventBus {
	return s.eventBus
}

// TopologyService returns the topology service.
func (s *Server) TopologyService() *service.TopologyService {
	return s.topology
}

// Close closes the server and releases resources.
func (s *Server) Close() {
	if s.stopBackground != nil {
		s.stopBackground()
	}
	s.eventBus.Close()
}
