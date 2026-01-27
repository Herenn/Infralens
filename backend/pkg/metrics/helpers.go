package metrics

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Compiled regexes for path normalization (compiled once for performance)
var (
	// UUID pattern: 8-4-4-4-12 hex digits with optional dashes
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

	// Short UUID/hash pattern: 8+ hex characters
	shortHashPattern = regexp.MustCompile(`[0-9a-fA-F]{8,}`)

	// Numeric ID pattern: pure integers
	numericIDPattern = regexp.MustCompile(`^\d+$`)

	// Kubernetes-style names: name-hash (e.g., nginx-7d4f8b9c5d-x2k9p)
	k8sNamePattern = regexp.MustCompile(`^[a-z0-9-]+-[a-z0-9]{5,10}$`)

	// Generic slug with trailing ID: name-123, service-abc123
	slugWithIDPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*-[a-zA-Z0-9]+$`)

	// IP address pattern
	ipPattern = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)

	// Known static path segments that should NOT be normalized
	staticSegments = map[string]bool{
		"api": true, "v1": true, "v2": true,
		"services": true, "connections": true, "nodes": true,
		"topology": true, "metrics": true, "stats": true,
		"events": true, "health": true, "ready": true,
		"ws": true, "websocket": true, "graph": true,
		"k8s": true, "status": true, "version": true,
		"inspect": true, "ai": true, "analyze": true,
	}
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
// It replaces UUIDs, numeric IDs, hashes, and dynamic slugs with placeholders.
func NormalizePath(path string) string {
	if path == "" {
		return "/"
	}

	// Split path into segments
	segments := strings.Split(strings.Trim(path, "/"), "/")
	normalized := make([]string, 0, len(segments))

	for _, segment := range segments {
		if segment == "" {
			continue
		}

		// Check if this is a known static segment
		if staticSegments[strings.ToLower(segment)] {
			normalized = append(normalized, segment)
			continue
		}

		// Normalize dynamic segments
		normalizedSegment := normalizeSegment(segment)
		normalized = append(normalized, normalizedSegment)
	}

	if len(normalized) == 0 {
		return "/"
	}

	return "/" + strings.Join(normalized, "/")
}

// normalizeSegment normalizes a single path segment.
func normalizeSegment(segment string) string {
	// Check for full UUID
	if uuidPattern.MatchString(segment) {
		return ":uuid"
	}

	// Check for pure numeric ID
	if numericIDPattern.MatchString(segment) {
		return ":id"
	}

	// Check for IP address
	if ipPattern.MatchString(segment) {
		return ":ip"
	}

	// Check for Kubernetes-style pod/deployment names (name-randomhash)
	if k8sNamePattern.MatchString(segment) {
		// Extract the base name before the hash
		parts := strings.Split(segment, "-")
		if len(parts) >= 2 {
			// Keep first part(s), replace last hash part
			return strings.Join(parts[:len(parts)-1], "-") + "-:hash"
		}
		return ":k8s_name"
	}

	// Check for short hashes (8+ hex chars that aren't other patterns)
	if len(segment) >= 8 && shortHashPattern.MatchString(segment) && isHexString(segment) {
		return ":hash"
	}

	// Check for generic slug with ID suffix (service-123, node-abc)
	if slugWithIDPattern.MatchString(segment) {
		parts := strings.Split(segment, "-")
		if len(parts) == 2 {
			// Check if second part looks like an ID
			if numericIDPattern.MatchString(parts[1]) || isHexString(parts[1]) {
				return parts[0] + "-:id"
			}
		}
	}

	// If segment contains mostly dynamic-looking content, normalize it
	// This catches things like "svc-a1b2c3d4" that don't match other patterns
	if looksLikeDynamicID(segment) {
		return ":id"
	}

	// Keep segment as-is if it looks static
	return segment
}

// isHexString checks if a string contains only hex characters.
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// looksLikeDynamicID checks if a segment looks like a dynamic ID.
func looksLikeDynamicID(segment string) bool {
	// If it contains both letters and numbers and has significant length,
	// it might be a dynamic ID
	hasLetter := false
	hasDigit := false
	for _, c := range segment {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}

	// Mixed alphanumeric strings longer than 6 chars are likely IDs
	if hasLetter && hasDigit && len(segment) > 6 {
		return true
	}

	return false
}
