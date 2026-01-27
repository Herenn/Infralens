package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// newTestStore creates an in-memory SQLite store for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	cfg := storage.Config{
		Driver:      "sqlite",
		DSN:         ":memory:",
		AutoMigrate: true,
	}

	store, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}

	t.Cleanup(func() {
		store.Close()
	})

	return store
}

// =============================================================================
// ServiceRepository Tests
// =============================================================================

func TestServiceRepo_Upsert_Insert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	svc := &storage.Service{
		ID:           "test-service-1",
		Name:         "test-service",
		DisplayName:  "Test Service",
		ResolvedName: "Pod: default/test-pod",
		Type:         "web_server",
		Tech:         "nginx",
		Icon:         "server",
		Namespace:    "default",
		Node:         "node-1",
		PodIP:        "10.0.0.1",
		Labels:       map[string]string{"app": "test"},
		Healthy:      true,
	}

	// Insert
	err := store.Services().Upsert(ctx, svc)
	if err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Verify
	got, err := store.Services().Get(ctx, svc.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Expected service, got nil")
	}
	if got.Name != svc.Name {
		t.Errorf("Name mismatch: got %q, want %q", got.Name, svc.Name)
	}
	if got.Type != svc.Type {
		t.Errorf("Type mismatch: got %q, want %q", got.Type, svc.Type)
	}
	if got.Labels["app"] != "test" {
		t.Errorf("Labels mismatch: got %v", got.Labels)
	}
}

func TestServiceRepo_Upsert_Update(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert initial service
	svc := &storage.Service{
		ID:      "test-service-2",
		Name:    "original-name",
		Type:    "web_server",
		Healthy: true,
	}
	if err := store.Services().Upsert(ctx, svc); err != nil {
		t.Fatalf("Initial Upsert failed: %v", err)
	}

	// Update with new values
	svc.Name = "updated-name"
	svc.Tech = "nginx"
	if err := store.Services().Upsert(ctx, svc); err != nil {
		t.Fatalf("Update Upsert failed: %v", err)
	}

	// Verify
	got, err := store.Services().Get(ctx, svc.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Name != "updated-name" {
		t.Errorf("Name not updated: got %q", got.Name)
	}
	if got.Tech != "nginx" {
		t.Errorf("Tech not updated: got %q", got.Tech)
	}
}

func TestServiceRepo_Upsert_PreservesExistingValues(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert with full data
	svc := &storage.Service{
		ID:        "test-service-3",
		Name:      "full-service",
		Type:      "database",
		Tech:      "postgresql",
		Namespace: "production",
		Healthy:   true,
	}
	if err := store.Services().Upsert(ctx, svc); err != nil {
		t.Fatalf("Initial Upsert failed: %v", err)
	}

	// Update with partial data (empty strings should not overwrite)
	update := &storage.Service{
		ID:      "test-service-3",
		Name:    "", // Empty - should preserve original
		Healthy: true,
	}
	if err := store.Services().Upsert(ctx, update); err != nil {
		t.Fatalf("Partial Upsert failed: %v", err)
	}

	// Verify original values preserved
	got, err := store.Services().Get(ctx, svc.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Name != "full-service" {
		t.Errorf("Name was overwritten: got %q", got.Name)
	}
	if got.Tech != "postgresql" {
		t.Errorf("Tech was overwritten: got %q", got.Tech)
	}
}

func TestServiceRepo_Get_NotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	got, err := store.Services().Get(ctx, "non-existent")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != nil {
		t.Errorf("Expected nil for non-existent service, got %+v", got)
	}
}

func TestServiceRepo_List(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert multiple services
	services := []*storage.Service{
		{ID: "svc-1", Name: "service-a", Node: "node-1", Namespace: "default", Healthy: true},
		{ID: "svc-2", Name: "service-b", Node: "node-1", Namespace: "production", Healthy: true},
		{ID: "svc-3", Name: "service-c", Node: "node-2", Namespace: "default", Healthy: true},
	}
	for _, svc := range services {
		if err := store.Services().Upsert(ctx, svc); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	tests := []struct {
		name     string
		filter   storage.ServiceFilter
		wantLen  int
		wantIDs  []string
	}{
		{
			name:    "no filter",
			filter:  storage.ServiceFilter{},
			wantLen: 3,
		},
		{
			name:    "filter by node",
			filter:  storage.ServiceFilter{Node: "node-1"},
			wantLen: 2,
		},
		{
			name:    "filter by namespace",
			filter:  storage.ServiceFilter{Namespace: "default"},
			wantLen: 2,
		},
		{
			name:    "filter with limit",
			filter:  storage.ServiceFilter{Limit: 2},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Services().List(ctx, tt.filter)
			if err != nil {
				t.Fatalf("List failed: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("List returned %d services, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestServiceRepo_Delete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert
	svc := &storage.Service{ID: "delete-me", Name: "to-delete", Healthy: true}
	if err := store.Services().Upsert(ctx, svc); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Delete
	if err := store.Services().Delete(ctx, svc.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	got, err := store.Services().Get(ctx, svc.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != nil {
		t.Errorf("Service still exists after delete")
	}
}

func TestServiceRepo_DeleteStale(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert services with different last_seen times
	now := time.Now()
	services := []*storage.Service{
		{ID: "fresh-1", Name: "fresh", Healthy: true},
		{ID: "stale-1", Name: "stale", Healthy: true},
	}
	for _, svc := range services {
		if err := store.Services().Upsert(ctx, svc); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Manually update last_seen for stale service
	_, err := store.db.ExecContext(ctx,
		"UPDATE services SET last_seen = ? WHERE id = ?",
		now.Add(-1*time.Hour), "stale-1")
	if err != nil {
		t.Fatalf("Failed to update last_seen: %v", err)
	}

	// Delete stale (older than 30 minutes)
	cutoff := now.Add(-30 * time.Minute)
	deleted, err := store.Services().DeleteStale(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteStale failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("DeleteStale returned %d, want 1", deleted)
	}

	// Verify stale is gone, fresh remains
	stale, _ := store.Services().Get(ctx, "stale-1")
	if stale != nil {
		t.Error("Stale service still exists")
	}

	fresh, _ := store.Services().Get(ctx, "fresh-1")
	if fresh == nil {
		t.Error("Fresh service was incorrectly deleted")
	}
}

func TestServiceRepo_Count(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Initially empty
	count, err := store.Services().Count(ctx)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Initial count = %d, want 0", count)
	}

	// Add services
	for i := 0; i < 5; i++ {
		svc := &storage.Service{ID: string(rune('a' + i)), Name: "test", Healthy: true}
		store.Services().Upsert(ctx, svc)
	}

	count, err = store.Services().Count(ctx)
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 5 {
		t.Errorf("Count = %d, want 5", count)
	}
}

// =============================================================================
// ConnectionRepository Tests
// =============================================================================

func TestConnectionRepo_Upsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	conn := &storage.Connection{
		ID:            "conn-1",
		SourceID:      "svc-a",
		TargetID:      "svc-b",
		Port:          8080,
		Count:         1,
		BytesSent:     1000,
		BytesRecv:     2000,
		BytesSentRate: 100.5,
		BytesRecvRate: 200.5,
	}

	// Insert
	if err := store.Connections().Upsert(ctx, conn); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Verify
	got, err := store.Connections().Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("Expected connection, got nil")
	}
	if got.Port != 8080 {
		t.Errorf("Port mismatch: got %d, want %d", got.Port, 8080)
	}
	if got.BytesSent != 1000 {
		t.Errorf("BytesSent mismatch: got %d, want %d", got.BytesSent, 1000)
	}
}

func TestConnectionRepo_Upsert_IncrementsCount(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	conn := &storage.Connection{
		ID:       "conn-count",
		SourceID: "svc-a",
		TargetID: "svc-b",
		Port:     80,
		Count:    1,
	}

	// Insert first
	if err := store.Connections().Upsert(ctx, conn); err != nil {
		t.Fatalf("First Upsert failed: %v", err)
	}

	// Upsert again (should increment count)
	if err := store.Connections().Upsert(ctx, conn); err != nil {
		t.Fatalf("Second Upsert failed: %v", err)
	}

	got, _ := store.Connections().Get(ctx, conn.ID)
	if got.Count != 2 {
		t.Errorf("Count = %d, want 2", got.Count)
	}
}

func TestConnectionRepo_List(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert connections
	conns := []*storage.Connection{
		{ID: "c1", SourceID: "a", TargetID: "b", Port: 80},
		{ID: "c2", SourceID: "a", TargetID: "c", Port: 443},
		{ID: "c3", SourceID: "b", TargetID: "c", Port: 80},
	}
	for _, c := range conns {
		store.Connections().Upsert(ctx, c)
	}

	tests := []struct {
		name    string
		filter  storage.ConnectionFilter
		wantLen int
	}{
		{name: "no filter", filter: storage.ConnectionFilter{}, wantLen: 3},
		{name: "by source", filter: storage.ConnectionFilter{SourceID: "a"}, wantLen: 2},
		{name: "by target", filter: storage.ConnectionFilter{TargetID: "c"}, wantLen: 2},
		{name: "by port", filter: storage.ConnectionFilter{Port: 80}, wantLen: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.Connections().List(ctx, tt.filter)
			if err != nil {
				t.Fatalf("List failed: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Errorf("Got %d connections, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestConnectionRepo_UpdateStats(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert connection
	conn := &storage.Connection{
		ID:       "stats-conn",
		SourceID: "a",
		TargetID: "b",
		Port:     80,
	}
	store.Connections().Upsert(ctx, conn)

	// Update stats
	err := store.Connections().UpdateStats(ctx, conn.ID, 5000, 10000, 500.0, 1000.0, 50, 100)
	if err != nil {
		t.Fatalf("UpdateStats failed: %v", err)
	}

	// Verify
	got, _ := store.Connections().Get(ctx, conn.ID)
	if got.BytesSent != 5000 {
		t.Errorf("BytesSent = %d, want 5000", got.BytesSent)
	}
	if got.BytesRecvRate != 1000.0 {
		t.Errorf("BytesRecvRate = %f, want 1000.0", got.BytesRecvRate)
	}
}

// =============================================================================
// MetricsRepository Tests
// =============================================================================

func TestMetricsRepo_Upsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	m := &storage.NodeMetrics{
		NodeName:   "node-1",
		CPUPercent: 45.5,
		MemPercent: 60.0,
		MemUsed:    8000000000,
		MemTotal:   16000000000,
	}

	if err := store.Metrics().Upsert(ctx, m); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	got, err := store.Metrics().Get(ctx, m.NodeName)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.CPUPercent != 45.5 {
		t.Errorf("CPUPercent = %f, want 45.5", got.CPUPercent)
	}
}

func TestMetricsRepo_List(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert metrics
	nodes := []string{"node-1", "node-2", "node-3"}
	for _, n := range nodes {
		store.Metrics().Upsert(ctx, &storage.NodeMetrics{NodeName: n, CPUPercent: 50.0})
	}

	got, err := store.Metrics().List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Got %d metrics, want 3", len(got))
	}
}

// =============================================================================
// Store Tests
// =============================================================================

func TestStore_GetTopology(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Populate data
	store.Services().Upsert(ctx, &storage.Service{ID: "svc-1", Name: "a", Healthy: true})
	store.Services().Upsert(ctx, &storage.Service{ID: "svc-2", Name: "b", Healthy: true})
	store.Connections().Upsert(ctx, &storage.Connection{ID: "c1", SourceID: "svc-1", TargetID: "svc-2", Port: 80})
	store.Metrics().Upsert(ctx, &storage.NodeMetrics{NodeName: "node-1", CPUPercent: 50.0})

	// Get topology
	topo, err := store.GetTopology(ctx)
	if err != nil {
		t.Fatalf("GetTopology failed: %v", err)
	}

	if len(topo.Services) != 2 {
		t.Errorf("Got %d services, want 2", len(topo.Services))
	}
	if len(topo.Connections) != 1 {
		t.Errorf("Got %d connections, want 1", len(topo.Connections))
	}
	if len(topo.NodeMetrics) != 1 {
		t.Errorf("Got %d node metrics, want 1", len(topo.NodeMetrics))
	}
}

func TestStore_Prune(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert data
	store.Services().Upsert(ctx, &storage.Service{ID: "fresh", Name: "fresh", Healthy: true})
	store.Services().Upsert(ctx, &storage.Service{ID: "stale", Name: "stale", Healthy: true})
	store.Connections().Upsert(ctx, &storage.Connection{ID: "c-fresh", SourceID: "a", TargetID: "b", Port: 80})
	store.Connections().Upsert(ctx, &storage.Connection{ID: "c-stale", SourceID: "x", TargetID: "y", Port: 80})
	store.Metrics().Upsert(ctx, &storage.NodeMetrics{NodeName: "fresh-node", CPUPercent: 50.0})
	store.Metrics().Upsert(ctx, &storage.NodeMetrics{NodeName: "stale-node", CPUPercent: 50.0})

	// Make some data stale
	now := time.Now()
	oldTime := now.Add(-1 * time.Hour)
	store.db.ExecContext(ctx, "UPDATE services SET last_seen = ? WHERE id = ?", oldTime, "stale")
	store.db.ExecContext(ctx, "UPDATE connections SET last_seen = ? WHERE id = ?", oldTime, "c-stale")
	store.db.ExecContext(ctx, "UPDATE node_metrics SET last_seen = ? WHERE node_name = ?", oldTime, "stale-node")

	// Prune
	pruned, err := store.Prune(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if pruned != 3 {
		t.Errorf("Pruned %d items, want 3", pruned)
	}

	// Verify fresh data remains
	svc, _ := store.Services().Get(ctx, "fresh")
	if svc == nil {
		t.Error("Fresh service was incorrectly pruned")
	}
	conn, _ := store.Connections().Get(ctx, "c-fresh")
	if conn == nil {
		t.Error("Fresh connection was incorrectly pruned")
	}
	metrics, _ := store.Metrics().Get(ctx, "fresh-node")
	if metrics == nil {
		t.Error("Fresh metrics was incorrectly pruned")
	}
}

func TestStore_RunInTransaction(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Successful transaction
	err := store.RunInTransaction(ctx, func(txCtx context.Context) error {
		store.Services().Upsert(txCtx, &storage.Service{ID: "tx-svc", Name: "tx", Healthy: true})
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction failed: %v", err)
	}

	// Verify committed
	svc, _ := store.Services().Get(ctx, "tx-svc")
	if svc == nil {
		t.Error("Transaction was not committed")
	}
}

func TestStore_RunInTransaction_Rollback(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Transaction that returns error (should rollback)
	testErr := storage.ErrNotFound
	err := store.RunInTransaction(ctx, func(txCtx context.Context) error {
		store.Services().Upsert(txCtx, &storage.Service{ID: "rollback-svc", Name: "rollback", Healthy: true})
		return testErr // Return error to trigger rollback
	})
	if err != testErr {
		t.Errorf("Expected error %v, got %v", testErr, err)
	}

	// Verify rolled back
	svc, _ := store.Services().Get(ctx, "rollback-svc")
	if svc != nil {
		t.Error("Transaction was not rolled back")
	}
}

func TestStore_Ping(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}
