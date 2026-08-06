package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// testServer holds test infrastructure
type testServer struct {
	store     storage.Store
	topology  *service.TopologyService
	processor *service.EventProcessor
	eventBus  *service.EventBus
	router    *mux.Router
}

// newTestServer creates a test server with in-memory SQLite
func newTestServer(t *testing.T) *testServer {
	t.Helper()

	// Create in-memory store
	store, err := sqlite.New(storage.Config{
		Driver:      "sqlite",
		DSN:         ":memory:",
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Create services
	eventBus := service.NewEventBus(100)
	topology := service.NewTopologyService(store, eventBus)
	processor := service.NewEventProcessor(topology, nil) // No K8s watcher in tests

	// Create router
	router := mux.NewRouter()
	api := router.PathPrefix("/api/v1").Subrouter()

	// Register handlers
	eventHandler := handlers.NewEventHandler(processor)
	topologyHandler := handlers.NewTopologyHandler(topology)

	api.HandleFunc("/events", eventHandler.HandleEvents).Methods("POST")
	api.HandleFunc("/stats", eventHandler.HandleStats).Methods("POST")
	api.HandleFunc("/metrics", eventHandler.HandleMetrics).Methods("POST")
	api.HandleFunc("/inspection", eventHandler.HandleInspection).Methods("POST")
	api.HandleFunc("/topology", topologyHandler.HandleGetTopology).Methods("GET")
	api.HandleFunc("/topology/history/range", topologyHandler.HandleGetHistoryRange).Methods("GET")
	api.HandleFunc("/topology/history/stale", topologyHandler.HandleGetStaleServices).Methods("GET")
	api.HandleFunc("/topology/history/diff", topologyHandler.HandleGetTopologyDiff).Methods("GET")
	api.HandleFunc("/services", topologyHandler.HandleGetServices).Methods("GET")
	// Route order/pattern mirrors router.go: /impact must be registered
	// before the bare route, and {id} must be greedy to match IDs
	// containing a literal "/" - see the comment there for why.
	api.HandleFunc("/services/{id:.+}/impact", topologyHandler.HandleGetImpact).Methods("GET")
	api.HandleFunc("/services/{id:.+}", topologyHandler.HandleGetService).Methods("GET")
	api.HandleFunc("/graph/stats", topologyHandler.HandleGetStats).Methods("GET")
	api.HandleFunc("/graph/criticality", topologyHandler.HandleGetCriticality).Methods("GET")
	api.HandleFunc("/graph/orphans", topologyHandler.HandleGetOrphans).Methods("GET")

	t.Cleanup(func() {
		eventBus.Close()
		store.Close()
	})

	return &testServer{
		store:     store,
		topology:  topology,
		processor: processor,
		eventBus:  eventBus,
		router:    router,
	}
}

// newTestServerWithHistory is newTestServer with topology history recording
// turned on, for exercising the ?at= and /topology/history/range endpoints
// that 400 without it.
func newTestServerWithHistory(t *testing.T) *testServer {
	t.Helper()
	ts := newTestServer(t)
	ts.topology.EnableHistory(5 * time.Minute)
	return ts
}

// =============================================================================
// Event Ingestion Tests
// =============================================================================

func TestHandleEvents_ValidBatch(t *testing.T) {
	ts := newTestServer(t)

	batch := map[string]interface{}{
		"node_name": "test-node",
		"timestamp": time.Now().Format(time.RFC3339),
		"events": []map[string]interface{}{
			{
				"pid":       1234,
				"comm":      "nginx",
				"src_addr":  "10.0.0.1",
				"dst_addr":  "10.0.0.2",
				"dst_port":  80,
				"direction": 0,
			},
		},
	}

	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}

	// Verify response
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Errorf("Expected status 'accepted', got %q", resp["status"])
	}
}

func TestHandleEvents_InvalidJSON(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestHandleEvents_EmptyBatch(t *testing.T) {
	ts := newTestServer(t)

	batch := map[string]interface{}{
		"node_name": "test-node",
		"timestamp": time.Now().Format(time.RFC3339),
		"events":    []interface{}{},
	}

	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	// Empty batch is still accepted
	if rr.Code != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
}

// =============================================================================
// Throughput Stats Tests
// =============================================================================

func TestHandleStats_ValidReport(t *testing.T) {
	ts := newTestServer(t)

	report := map[string]interface{}{
		"node_name":   "test-node",
		"timestamp":   time.Now().Format(time.RFC3339),
		"interval_ms": 1000,
		"connections": []map[string]interface{}{
			{
				"pid":             1234,
				"comm":            "nginx",
				"src_addr":        "10.0.0.1",
				"dst_addr":        "10.0.0.2",
				"src_port":        54321,
				"dst_port":        80,
				"bytes_sent":      1000,
				"bytes_recv":      2000,
				"bytes_sent_rate": 100.5,
				"bytes_recv_rate": 200.5,
			},
		},
	}

	body, _ := json.Marshal(report)
	req := httptest.NewRequest("POST", "/api/v1/stats", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}
}

// =============================================================================
// Host Metrics Tests
// =============================================================================

func TestHandleMetrics_Valid(t *testing.T) {
	ts := newTestServer(t)

	metrics := map[string]interface{}{
		"node_name":   "test-node",
		"cpu_percent": 45.5,
		"mem_percent": 60.0,
		"mem_used":    8000000000,
		"mem_total":   16000000000,
	}

	body, _ := json.Marshal(metrics)
	req := httptest.NewRequest("POST", "/api/v1/metrics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}
}

// =============================================================================
// Topology Query Tests
// =============================================================================

func TestHandleGetTopology_Empty(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify JSON structure
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if _, ok := resp["services"]; !ok {
		t.Error("Response missing 'services' field")
	}
	if _, ok := resp["connections"]; !ok {
		t.Error("Response missing 'connections' field")
	}
	if _, ok := resp["updated_at"]; !ok {
		t.Error("Response missing 'updated_at' field")
	}
}

func TestHandleGetTopology_WithData(t *testing.T) {
	ts := newTestServer(t)

	// First, ingest some events
	batch := map[string]interface{}{
		"node_name": "test-node",
		"timestamp": time.Now().Format(time.RFC3339),
		"events": []map[string]interface{}{
			{
				"pid":       1234,
				"comm":      "nginx",
				"src_addr":  "10.0.0.1",
				"dst_addr":  "10.0.0.2",
				"dst_port":  80,
				"direction": 0,
			},
		},
	}

	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	// Now get topology
	req = httptest.NewRequest("GET", "/api/v1/topology", nil)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	services := resp["services"].([]interface{})
	if len(services) == 0 {
		t.Error("Expected services after ingesting events")
	}
}

// =============================================================================
// Services API Tests
// =============================================================================

func TestHandleGetServices(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// Verify it's a JSON array
	var resp []interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
}

func TestHandleGetService_NotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/services/non-existent", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

// TestHandleGetService_IDContainsSlash guards against a routing regression:
// service IDs routinely contain a literal "/" (e.g. "10.0.1.10/nginx", the
// IP+process-name format processor.go generates for non-K8s services), and
// gorilla/mux's default {id} pattern only matches a single path segment.
// Without the greedy {id:.+} pattern in router.go, this 404s even though the
// service exists.
func TestHandleGetService_IDContainsSlash(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "10.0.1.10/nginx")

	req := httptest.NewRequest("GET", "/api/v1/services/10.0.1.10%2Fnginx", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if resp["id"] != "10.0.1.10/nginx" {
		t.Errorf("id = %v, want 10.0.1.10/nginx", resp["id"])
	}
}

// =============================================================================
// Graph Stats Tests
// =============================================================================

func TestHandleGetStats(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/graph/stats", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if _, ok := resp["services"]; !ok {
		t.Error("Response missing 'services' count")
	}
	if _, ok := resp["connections"]; !ok {
		t.Error("Response missing 'connections' count")
	}
}

func TestHandleGetCriticality_RanksDescending(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B", "C", "D")

	req := httptest.NewRequest("GET", "/api/v1/graph/criticality", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var resp []handlers.CriticalityResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(resp) != 4 || resp[0].ID != "D" || resp[0].BlastRadius != 3 {
		t.Errorf("expected D first with blast_radius=3, got %+v", resp)
	}
}

func TestHandleGetCriticality_InvalidLimit(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B")

	req := httptest.NewRequest("GET", "/api/v1/graph/criticality?limit=0", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetOrphans_ReturnsOnlyUnconnected(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B")
	if err := ts.topology.AddOrUpdateService(context.Background(), &storage.Service{ID: "Z", Name: "isolated", LastSeen: time.Now()}); err != nil {
		t.Fatalf("adding isolated service: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/graph/orphans", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var resp []handlers.ServiceResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(resp) != 1 || resp[0].ID != "Z" {
		t.Errorf("expected exactly [Z], got %+v", resp)
	}
}

// =============================================================================
// End-to-End Flow Tests
// =============================================================================

func TestE2E_IngestAndQuery(t *testing.T) {
	ts := newTestServer(t)

	// Step 1: Check initial state is empty
	req := httptest.NewRequest("GET", "/api/v1/graph/stats", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	var stats map[string]int
	json.NewDecoder(rr.Body).Decode(&stats)
	if stats["services"] != 0 {
		t.Errorf("Initial services count = %d, want 0", stats["services"])
	}

	// Step 2: Ingest TCP events
	batch := map[string]interface{}{
		"node_name": "prod-server-1",
		"timestamp": time.Now().Format(time.RFC3339),
		"events": []map[string]interface{}{
			{"pid": 1001, "comm": "frontend", "src_addr": "10.0.0.1", "dst_addr": "10.0.0.2", "dst_port": 8080, "direction": 0},
			{"pid": 2001, "comm": "backend", "src_addr": "10.0.0.2", "dst_addr": "10.0.0.3", "dst_port": 5432, "direction": 0},
		},
	}

	body, _ := json.Marshal(batch)
	req = httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Event ingestion failed: %d", rr.Code)
	}

	// Step 3: Ingest metrics
	metrics := map[string]interface{}{
		"node_name":   "prod-server-1",
		"cpu_percent": 45.5,
		"mem_percent": 60.0,
		"mem_used":    8000000000,
		"mem_total":   16000000000,
	}
	body, _ = json.Marshal(metrics)
	req = httptest.NewRequest("POST", "/api/v1/metrics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	// Step 4: Verify topology is populated
	req = httptest.NewRequest("GET", "/api/v1/topology", nil)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	var topo map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&topo); err != nil {
		t.Fatalf("Failed to decode topology: %v", err)
	}

	services := topo["services"].([]interface{})
	if len(services) < 2 {
		t.Errorf("Expected at least 2 services, got %d", len(services))
	}

	connections := topo["connections"].([]interface{})
	if len(connections) < 1 {
		t.Errorf("Expected at least 1 connection, got %d", len(connections))
	}

	nodeMetrics := topo["node_metrics"].(map[string]interface{})
	if _, ok := nodeMetrics["prod-server-1"]; !ok {
		t.Error("Node metrics not found for prod-server-1")
	}

	// Step 5: Verify stats endpoint
	req = httptest.NewRequest("GET", "/api/v1/graph/stats", nil)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	json.NewDecoder(rr.Body).Decode(&stats)
	if stats["services"] < 2 {
		t.Errorf("Stats services = %d, want at least 2", stats["services"])
	}
}

// =============================================================================
// Error Handling Tests
// =============================================================================

func TestHandleEvents_MissingNodeName(t *testing.T) {
	ts := newTestServer(t)

	// Missing node_name field
	batch := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"events":    []interface{}{},
	}

	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	// Still accepted (node_name can be empty)
	if rr.Code != http.StatusAccepted {
		t.Errorf("Expected status %d, got %d", http.StatusAccepted, rr.Code)
	}
}

func TestHandleMetrics_InvalidJSON(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("POST", "/api/v1/metrics", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

// =============================================================================
// Content-Type Tests
// =============================================================================

func TestTopology_ReturnsJSON(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

func TestServices_ReturnsJSON(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/services", nil)
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

// =============================================================================
// Inspection Ingestion Tests
// =============================================================================

// TestHandleInspection_MissingFields is a regression test: a report without an
// "inspection" object used to nil-deref in the service layer, panicking the
// handler instead of rejecting the request.
func TestHandleInspection_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no inspection object", `{"node_name":"n1","service_id":"10.0.0.1/nginx"}`},
		{"null inspection object", `{"node_name":"n1","service_id":"10.0.0.1/nginx","inspection":null}`},
		{"no service id", `{"node_name":"n1","inspection":{"PID":1}}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)

			req := httptest.NewRequest("POST", "/api/v1/inspection", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			ts.router.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandleInspection_Valid(t *testing.T) {
	ts := newTestServer(t)

	body := `{"node_name":"n1","service_id":"10.0.0.1/nginx","inspection":{"PID":42,"ProcessName":"nginx"}}`
	req := httptest.NewRequest("POST", "/api/v1/inspection", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}

	insp, err := ts.store.Services().GetInspection(t.Context(), "10.0.0.1/nginx")
	if err != nil {
		t.Fatalf("GetInspection: %v", err)
	}
	if insp == nil {
		t.Fatal("expected inspection to be stored")
	}
	if insp.ProcessName != "nginx" {
		t.Errorf("ProcessName = %q, want %q", insp.ProcessName, "nginx")
	}
}

// =============================================================================
// Impact (Blast Radius) API Tests
// =============================================================================

// seedChain wires services and connections directly through the topology
// service rather than HTTP event ingestion - ingestion derives service IDs
// from IP/process-name heuristics, which makes an intentional graph shape
// like a chain awkward to construct; a direct call gives a deterministic,
// readable graph to assert against.
func seedChain(t *testing.T, ts *testServer, names ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	for _, name := range names {
		if err := ts.topology.AddOrUpdateService(ctx, &storage.Service{ID: name, Name: name, LastSeen: now}); err != nil {
			t.Fatalf("adding service %s: %v", name, err)
		}
	}
	for i := 0; i < len(names)-1; i++ {
		src, dst := names[i], names[i+1]
		if err := ts.topology.AddConnection(ctx, &storage.Connection{
			ID: src + "->" + dst, SourceID: src, TargetID: dst,
			Port: 80, Protocol: "tcp", LastSeen: now,
		}); err != nil {
			t.Fatalf("adding connection %s->%s: %v", src, dst, err)
		}
	}
}

func TestHandleGetImpact_Upstream(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B", "C", "D")

	req := httptest.NewRequest("GET", "/api/v1/services/C/impact?direction=upstream", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	services := resp["services"].([]interface{})
	if len(services) != 3 {
		t.Errorf("expected 3 services (A, B, C) upstream of C, got %d: %+v", len(services), services)
	}
}

// TestHandleGetImpact_IDContainsSlash is TestHandleGetService_IDContainsSlash
// for the impact route - the same routing regression risk, doubled: {id}
// must be greedy AND the /impact route must be registered before the bare
// one, or a greedy bare route swallows ".../impact" as part of the id.
func TestHandleGetImpact_IDContainsSlash(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "10.0.1.10/nginx", "10.0.2.10/api-gateway")

	req := httptest.NewRequest("GET", "/api/v1/services/10.0.1.10%2Fnginx/impact?direction=downstream", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	services := resp["services"].([]interface{})
	if len(services) != 2 {
		t.Errorf("expected 2 services, got %d: %+v", len(services), services)
	}
}

func TestHandleGetImpact_InvalidDirection(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B")

	req := httptest.NewRequest("GET", "/api/v1/services/A/impact?direction=sideways", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetImpact_InvalidDepth(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B")

	req := httptest.NewRequest("GET", "/api/v1/services/A/impact?depth=0", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetImpact_NotFound(t *testing.T) {
	ts := newTestServer(t)
	seedChain(t, ts, "A", "B")

	req := httptest.NewRequest("GET", "/api/v1/services/does-not-exist/impact", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected status %d, got %d: %s", http.StatusNotFound, rr.Code, rr.Body.String())
	}
}

// =============================================================================
// History API Tests
// =============================================================================

func TestHandleGetTopologyDiff_MissingParams(t *testing.T) {
	ts := newTestServerWithHistory(t)

	req := httptest.NewRequest("GET", "/api/v1/topology/history/diff?from="+url.QueryEscape(time.Now().Format(time.RFC3339)), nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d with only 'from' set, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetTopologyDiff_HistoryDisabled(t *testing.T) {
	ts := newTestServer(t)

	from := url.QueryEscape(time.Now().Add(-time.Hour).Format(time.RFC3339))
	to := url.QueryEscape(time.Now().Format(time.RFC3339))
	req := httptest.NewRequest("GET", "/api/v1/topology/history/diff?from="+from+"&to="+to, nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetTopologyDiff_FindsChanges(t *testing.T) {
	ts := newTestServerWithHistory(t)
	ctx := context.Background()

	t0 := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	t1 := t0.Add(time.Hour)

	if err := ts.topology.AddOrUpdateService(ctx, &storage.Service{ID: "svc-old", Name: "old", LastSeen: t0}); err != nil {
		t.Fatalf("adding old service: %v", err)
	}
	if err := ts.topology.AddOrUpdateService(ctx, &storage.Service{ID: "svc-new", Name: "new", LastSeen: t1}); err != nil {
		t.Fatalf("adding new service: %v", err)
	}

	from := url.QueryEscape(t0.Format(time.RFC3339))
	to := url.QueryEscape(t1.Format(time.RFC3339))
	req := httptest.NewRequest("GET", "/api/v1/topology/history/diff?from="+from+"&to="+to, nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var resp handlers.TopologyDiffResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(resp.AddedServices) != 1 || resp.AddedServices[0].ID != "svc-new" {
		t.Errorf("AddedServices = %+v, want exactly [svc-new]", resp.AddedServices)
	}
	if len(resp.RemovedServices) != 1 || resp.RemovedServices[0].ID != "svc-old" {
		t.Errorf("RemovedServices = %+v, want exactly [svc-old]", resp.RemovedServices)
	}
}

func TestHandleGetTopology_AtParam_InvalidTimestamp(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology?at=not-a-timestamp", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetTopology_AtParam_HistoryDisabled(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology?at="+url.QueryEscape(time.Now().Format(time.RFC3339)), nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetTopology_AtParam_HistoryEnabled(t *testing.T) {
	ts := newTestServerWithHistory(t)

	req := httptest.NewRequest("GET", "/api/v1/topology?at="+url.QueryEscape(time.Now().Format(time.RFC3339)), nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if _, ok := resp["services"]; !ok {
		t.Error("Response missing 'services' field")
	}
}

func TestHandleGetHistoryRange_HistoryDisabled(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology/history/range", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetHistoryRange_Empty(t *testing.T) {
	ts := newTestServerWithHistory(t)

	req := httptest.NewRequest("GET", "/api/v1/topology/history/range", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp handlers.HistoryRangeResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if resp.Earliest != nil || resp.Latest != nil {
		t.Errorf("expected null bounds before anything is recorded, got %+v", resp)
	}
}

func TestHandleGetHistoryRange_WithData(t *testing.T) {
	ts := newTestServerWithHistory(t)

	batch := map[string]interface{}{
		"node_name": "test-node",
		"timestamp": time.Now().Format(time.RFC3339),
		"events": []map[string]interface{}{
			{
				"pid": 1234, "comm": "nginx",
				"src_addr": "10.0.0.1", "dst_addr": "10.0.0.2",
				"dst_port": 80, "direction": 0,
			},
		},
	}
	body, _ := json.Marshal(batch)
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("Expected status %d ingesting events, got %d: %s", http.StatusAccepted, rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/v1/topology/history/range", nil)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp handlers.HistoryRangeResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if resp.Earliest == nil || resp.Latest == nil {
		t.Fatalf("expected non-null bounds after recording an event, got %+v", resp)
	}
	if _, err := time.Parse(time.RFC3339, *resp.Earliest); err != nil {
		t.Errorf("earliest %q is not RFC3339: %v", *resp.Earliest, err)
	}
	if _, err := time.Parse(time.RFC3339, *resp.Latest); err != nil {
		t.Errorf("latest %q is not RFC3339: %v", *resp.Latest, err)
	}
}

func TestHandleGetStaleServices_HistoryDisabled(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/topology/history/stale", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetStaleServices_InvalidOlderThan(t *testing.T) {
	ts := newTestServerWithHistory(t)

	req := httptest.NewRequest("GET", "/api/v1/topology/history/stale?olderThan=not-a-duration", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestHandleGetStaleServices_ReturnsOnlyOldServices(t *testing.T) {
	ts := newTestServerWithHistory(t)
	ctx := context.Background()

	old := time.Now().Add(-72 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	if err := ts.topology.AddOrUpdateService(ctx, &storage.Service{ID: "old-svc", Name: "old", LastSeen: old}); err != nil {
		t.Fatalf("adding old service: %v", err)
	}
	if err := ts.topology.AddOrUpdateService(ctx, &storage.Service{ID: "recent-svc", Name: "recent", LastSeen: recent}); err != nil {
		t.Fatalf("adding recent service: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/topology/history/stale?olderThan=48h", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp []handlers.StaleServiceResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if len(resp) != 1 || resp[0].ID != "old-svc" {
		t.Errorf("expected exactly [old-svc], got %+v", resp)
	}
}

// =============================================================================
// Helper to read response body
// =============================================================================

func readBody(t *testing.T, body io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	return string(data)
}
