// Package api provides the HTTP API for InfraLens.
package api

import (
	"context"
	"net/http"
	"runtime"
	"sync"

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
	"github.com/Herenn/Infralens/backend/service/demo"
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

	// sessions is nil unless UI login is enabled (UI_PASSWORD).
	sessions *middleware.SessionManager

	// stopBackground cancels goroutines the server owns (drift detection).
	stopBackground context.CancelFunc
	background     context.Context
	driftOnce      sync.Once

	// mcp is the /mcp endpoint, nil when MCP_ENABLED=false. See mcp.go.
	mcp http.Handler
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

	topologySvc.ConfigureIdleCost(cfg.IdleCost)
	for _, w := range cfg.IdleCost.Warnings {
		log.Warn(w)
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
	// Logouts are persisted so they survive restarts and reach every
	// replica; both bundled stores support it.
	var revocations storage.SessionRevocationRepository
	if rs, ok := store.(storage.SessionRevocationStore); ok {
		revocations = rs.SessionRevocations()
	}
	s.sessions = middleware.NewSessionManager(cfg.Auth, cfg.CORS.AllowedOrigins, revocations)

	// Set build info for metrics
	metrics.SetBuildInfo(handlers.Version, runtime.Version())

	bgCtx, cancel := context.WithCancel(context.Background())
	s.stopBackground = cancel
	s.background = bgCtx
	// In demo mode the simulator writes a synthetic past at startup; the
	// detector must seed after it, so main starts it once that is done.
	if !cfg.Server.DemoMode {
		s.StartDriftDetection()
	}

	s.setupRoutes()
	return s
}

// StartDriftDetection starts drift detection, if configured. NewServer does
// this itself except in demo mode, where it must wait until the simulator
// has written its past (see main). Calling it again is a no-op.
func (s *Server) StartDriftDetection() {
	s.driftOnce.Do(func() { s.startDriftDetection(s.background) })
}

// startDriftDetection starts architecture drift detection. It runs whenever
// topology history is on (drift is computed from it), recording changes in
// the drift feed and pushing them to open UIs; a configured webhook is also
// notified.
func (s *Server) startDriftDetection(ctx context.Context) {
	if !s.config.Drift.DetectionEnabled {
		if s.config.Drift.Enabled() {
			log.Warn("DRIFT_WEBHOOK_URL is set but DRIFT_ENABLED=false - no drift notifications will be sent")
		}
		return
	}
	cfg := service.DriftConfig{
		Interval:  s.config.Drift.Interval,
		GoneAfter: s.config.Drift.GoneAfter,
		Warmup:    s.config.Drift.Warmup,
	}
	if s.config.Server.DemoMode {
		cfg = demo.TuneDriftConfig(cfg)
	}
	var notifier service.DriftNotifier
	if s.config.Drift.Enabled() {
		notifier = service.NewWebhookDriftNotifier(s.config.Drift.WebhookURL)
	}
	detector, err := s.topology.NewDriftDetector(cfg, notifier)
	if err != nil {
		if s.config.Drift.Enabled() {
			log.WithError(err).Warn("DRIFT_WEBHOOK_URL is set but drift detection could not start " +
				"(it requires HISTORY_ENABLED=true) - no drift notifications will be sent")
		}
		return
	}
	log.WithField("webhook", notifier != nil).Info("Architecture drift detection enabled")
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
	driftHandler := handlers.NewDriftHandler(s.topology)
	authHandler := handlers.NewAuthHandler(s.sessions)

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
	api.HandleFunc("/graph/idle-cost", topologyHandler.HandleGetIdleCost).Methods("GET")

	// Architecture drift feed. Not registered at all when switched off, so
	// it answers 404 and the UI hides it.
	if s.config.Drift.DetectionEnabled {
		api.HandleFunc("/drift/events", driftHandler.HandleListEvents).Methods("GET")
		api.HandleFunc("/drift/summary", driftHandler.HandleSummary).Methods("GET")
	}

	// WebSocket endpoint
	api.HandleFunc("/ws", wsHandler.HandleWebSocket)

	// AI documentation endpoints
	api.HandleFunc("/ai/status", aiHandler.HandleStatus).Methods("GET")
	api.HandleFunc("/ai/config", aiHandler.HandleConfig).Methods("GET", "POST")
	api.HandleFunc("/ai/docs", aiHandler.HandleDocs).Methods("POST")
	api.HandleFunc("/ai/ask", aiHandler.HandleAsk).Methods("POST")
	api.HandleFunc("/ai/providers", aiHandler.HandleProviders).Methods("GET")

	// UI login. Always public; with UI_PASSWORD set, every other route not
	// listed in middleware/uiauth.go requires a session or the API key.
	api.HandleFunc("/auth/login", authHandler.HandleLogin).Methods("POST")
	api.HandleFunc("/auth/logout", authHandler.HandleLogout).Methods("POST")
	api.HandleFunc("/auth/status", authHandler.HandleStatus).Methods("GET")
	// MCP endpoint for AI assistants. Not routed here: it is served ahead of
	// the global middleware with its own auth and CORS - see mcp.go.
	if s.config.MCP.Enabled {
		s.mcp = s.newMCPEndpoint()
	}

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
		middleware.UIAuth(s.config.Auth, s.sessions),
	)(s.router)

	return s.withMCP(handler)
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
	s.sessions.Close()
	s.eventBus.Close()
}
