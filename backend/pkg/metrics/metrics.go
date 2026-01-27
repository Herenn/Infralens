// Package metrics provides Prometheus metrics for InfraLens observability.
//
// # Metric Categories
//
// This package exposes the following metric categories:
//   - HTTP metrics: Request counts, latency, and sizes
//   - Database metrics: Query duration and counts
//   - Event processing metrics: Agent event throughput
//   - Topology metrics: Service and connection counts
//   - WebSocket metrics: Active connections and messages
//
// # Customizing Histogram Buckets
//
// The default histogram buckets are optimized for typical web service latencies.
// To customize buckets for your environment, modify the Buckets field in the
// respective histogram definitions below before the metrics are registered.
//
// Example custom buckets for high-latency environments:
//
//	[]float64{0.1, 0.5, 1, 2, 5, 10, 30, 60}
//
// Example custom buckets for low-latency environments:
//
//	[]float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05}
//
// # Default Bucket Configurations
//
// HTTP Request Duration: 1ms to 10s (suitable for API calls)
// DB Query Duration: 0.1ms to 1s (suitable for database operations)
// Event Processing: 0.1ms to 100ms (suitable for stream processing)
//
// # Environment Variable Support (Future)
//
// To enable runtime bucket configuration via environment variables,
// set METRICS_HTTP_BUCKETS or METRICS_DB_BUCKETS with comma-separated values.
// This feature requires application restart to take effect.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	namespace = "infralens"
	subsystem = "backend"
)

// Default histogram buckets - modify these for your environment
var (
	// DefaultHTTPBuckets are latency buckets for HTTP requests (in seconds).
	// Range: 1ms to 10s, suitable for typical API response times.
	DefaultHTTPBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

	// DefaultDBBuckets are latency buckets for database queries (in seconds).
	// Range: 0.1ms to 1s, suitable for indexed database operations.
	DefaultDBBuckets = []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, .5, 1}

	// DefaultEventBuckets are latency buckets for event processing (in seconds).
	// Range: 0.1ms to 100ms, suitable for stream processing.
	DefaultEventBuckets = []float64{.0001, .0005, .001, .005, .01, .025, .05, .1}
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
			Buckets:   DefaultHTTPBuckets,
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
			Buckets:   DefaultDBBuckets,
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
			Buckets:   DefaultEventBuckets,
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

// =============================================================================
// Helper Functions
// =============================================================================

// Timer helps measure operation duration.
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer starting now.
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// ObserveEvent records event processing metrics.
func (t *Timer) ObserveEvent(eventType string) {
	duration := time.Since(t.start).Seconds()
	EventProcessingDuration.WithLabelValues(eventType).Observe(duration)
}

// RecordEventBatch records event batch metrics.
func RecordEventBatch(eventType, node string, count int) {
	EventsProcessedTotal.WithLabelValues(eventType, node).Add(float64(count))
	EventBatchSize.WithLabelValues(eventType).Observe(float64(count))
}

// RecordWebSocketConnect records a WebSocket connection.
func RecordWebSocketConnect() {
	WebSocketConnectionsActive.Inc()
}

// RecordWebSocketDisconnect records a WebSocket disconnection.
func RecordWebSocketDisconnect() {
	WebSocketConnectionsActive.Dec()
}
