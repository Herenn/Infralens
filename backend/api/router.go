// Package api provides the HTTP API for InfraLens.
package api

import (
	"net/http"
	"runtime"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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
		topologySvc.EnableHistory(cfg.Storage.HistoryMaxGap)
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

	s.setupRoutes()
	return s
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
	api.HandleFunc("/services", topologyHandler.HandleGetServices).Methods("GET")
	api.HandleFunc("/services/{id}", topologyHandler.HandleGetService).Methods("GET")
	api.HandleFunc("/graph/stats", topologyHandler.HandleGetStats).Methods("GET")

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
	s.eventBus.Close()
}
