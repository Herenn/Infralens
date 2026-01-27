package metrics

import (
	"strconv"
	"time"
)

// Timer helps measure operation duration.
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer starting now.
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// ObserveHTTP records HTTP request metrics.
func (t *Timer) ObserveHTTP(method, path string, status int, responseSize int) {
	duration := time.Since(t.start).Seconds()
	statusStr := strconv.Itoa(status)

	HTTPRequestDuration.WithLabelValues(method, path).Observe(duration)
	HTTPRequestsTotal.WithLabelValues(method, path, statusStr).Inc()
	HTTPResponseSize.WithLabelValues(method, path).Observe(float64(responseSize))
}

// ObserveDB records database query metrics.
func (t *Timer) ObserveDB(operation, table string, err error) {
	duration := time.Since(t.start).Seconds()
	status := "success"
	if err != nil {
		status = "error"
	}

	DBQueryDuration.WithLabelValues(operation, table).Observe(duration)
	DBQueriesTotal.WithLabelValues(operation, table, status).Inc()
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

// RecordWebSocketMessage records a WebSocket message.
func RecordWebSocketMessage(msgType string) {
	WebSocketMessagesTotal.WithLabelValues(msgType).Inc()
}

// UpdateTopologyMetrics updates topology gauge metrics.
func UpdateTopologyMetrics(services, connections, nodes int64) {
	ServicesTotal.Set(float64(services))
	ConnectionsTotal.Set(float64(connections))
	NodesTotal.Set(float64(nodes))
}

// RecordPrunedItems records pruned items.
func RecordPrunedItems(itemType string, count int64) {
	PrunedItemsTotal.WithLabelValues(itemType).Add(float64(count))
}

// NormalizePath normalizes URL paths for metrics labels.
// This prevents high-cardinality labels from dynamic path segments.
func NormalizePath(path string) string {
	// Common patterns to normalize
	switch {
	case len(path) > 20 && path[:17] == "/api/v1/services/":
		return "/api/v1/services/{id}"
	default:
		return path
	}
}
