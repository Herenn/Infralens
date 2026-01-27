// Package metrics provides Prometheus metrics for InfraLens observability.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	namespace = "infralens"
	subsystem = "backend"
)

// HTTP metrics
var (
	// HTTPRequestsTotal counts total HTTP requests by method, path, and status.
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests by method, path, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	// HTTPRequestDuration measures HTTP request latency.
	HTTPRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency in seconds.",
			Buckets:   []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path"},
	)

	// HTTPRequestSize measures HTTP request body size.
	HTTPRequestSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_request_size_bytes",
			Help:      "HTTP request body size in bytes.",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 7), // 100B to 100MB
		},
		[]string{"method", "path"},
	)

	// HTTPResponseSize measures HTTP response body size.
	HTTPResponseSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_response_size_bytes",
			Help:      "HTTP response body size in bytes.",
			Buckets:   prometheus.ExponentialBuckets(100, 10, 7),
		},
		[]string{"method", "path"},
	)

	// HTTPActiveRequests tracks currently in-flight requests.
	HTTPActiveRequests = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "http_active_requests",
			Help:      "Number of HTTP requests currently being processed.",
		},
	)
)

// Database metrics
var (
	// DBQueryDuration measures database query latency.
	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "db_query_duration_seconds",
			Help:      "Database query latency in seconds.",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"operation", "table"},
	)

	// DBQueriesTotal counts total database queries.
	DBQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "db_queries_total",
			Help:      "Total number of database queries by operation and table.",
		},
		[]string{"operation", "table", "status"},
	)

	// DBConnectionsOpen tracks open database connections.
	DBConnectionsOpen = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "db_connections_open",
			Help:      "Number of open database connections.",
		},
	)

	// DBConnectionsInUse tracks in-use database connections.
	DBConnectionsInUse = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "db_connections_in_use",
			Help:      "Number of database connections currently in use.",
		},
	)
)

// Event processing metrics
var (
	// EventsProcessedTotal counts total events processed from agents.
	EventsProcessedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "events_processed_total",
			Help:      "Total number of events processed from agents.",
		},
		[]string{"type", "node"},
	)

	// EventProcessingDuration measures event processing time.
	EventProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "event_processing_duration_seconds",
			Help:      "Event processing duration in seconds.",
			Buckets:   []float64{.0001, .0005, .001, .005, .01, .025, .05, .1},
		},
		[]string{"type"},
	)

	// EventBatchSize tracks the size of event batches.
	EventBatchSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "event_batch_size",
			Help:      "Number of events per batch.",
			Buckets:   []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
		},
		[]string{"type"},
	)
)

// Topology metrics
var (
	// ServicesTotal tracks total discovered services.
	ServicesTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "services_total",
			Help:      "Total number of discovered services.",
		},
	)

	// ConnectionsTotal tracks total discovered connections.
	ConnectionsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "connections_total",
			Help:      "Total number of discovered connections.",
		},
	)

	// NodesTotal tracks total monitored nodes.
	NodesTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "nodes_total",
			Help:      "Total number of monitored nodes.",
		},
	)

	// PrunedItemsTotal counts pruned items.
	PrunedItemsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "pruned_items_total",
			Help:      "Total number of pruned items.",
		},
		[]string{"type"},
	)
)

// WebSocket metrics
var (
	// WebSocketConnectionsActive tracks active WebSocket connections.
	WebSocketConnectionsActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "websocket_connections_active",
			Help:      "Number of active WebSocket connections.",
		},
	)

	// WebSocketMessagesTotal counts WebSocket messages.
	WebSocketMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "websocket_messages_total",
			Help:      "Total number of WebSocket messages sent.",
		},
		[]string{"type"},
	)
)

// Build info metric
var (
	// BuildInfo exposes build information.
	BuildInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "build_info",
			Help:      "Build information for the InfraLens backend.",
		},
		[]string{"version", "go_version"},
	)
)

// SetBuildInfo sets the build information metric.
func SetBuildInfo(version, goVersion string) {
	BuildInfo.WithLabelValues(version, goVersion).Set(1)
}
