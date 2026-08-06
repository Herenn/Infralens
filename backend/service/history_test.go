package service

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

func newHistoryTestService(t *testing.T, enable bool) (*TopologyService, storage.Store) {
	t.Helper()

	store, err := sqlite.New(storage.Config{
		Driver:         "sqlite",
		DSN:            ":memory:",
		AutoMigrate:    true,
		HistoryEnabled: enable,
	})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := NewEventBus(64)
	t.Cleanup(bus.Close)

	ts := NewTopologyService(store, bus)
	if enable {
		ts.EnableHistory(5 * time.Minute)
	}
	return ts, store
}

// TestHistoryRecordedThroughTopologyService is the end-to-end check that
// history is actually written by the normal write path, not just by the
// storage layer in isolation. A history feature that works only when called
// directly is a history feature nobody gets.
func TestHistoryRecordedThroughTopologyService(t *testing.T) {
	ts, store := newHistoryTestService(t, true)
	ctx := context.Background()

	observed := time.Now().Add(-time.Hour).Truncate(time.Second)

	if err := ts.AddOrUpdateService(ctx, &storage.Service{
		ID:       "10.0.0.1/api",
		Name:     "api",
		Type:     "web_server",
		LastSeen: observed,
	}); err != nil {
		t.Fatalf("adding service: %v", err)
	}

	if err := ts.AddConnection(ctx, &storage.Connection{
		ID:       "10.0.0.1/api->10.0.0.2:5432",
		SourceID: "10.0.0.1/api",
		TargetID: "10.0.0.2:5432",
		Port:     5432,
		Protocol: "tcp",
		LastSeen: observed,
	}); err != nil {
		t.Fatalf("adding connection: %v", err)
	}

	services, err := store.History().ServicesAt(ctx, observed)
	if err != nil {
		t.Fatalf("reconstructing services: %v", err)
	}
	if len(services) != 1 || services[0].ServiceID != "10.0.0.1/api" {
		t.Fatalf("expected the service in history at its observation time, got %+v", services)
	}
	if services[0].Name != "api" || services[0].Type != "web_server" {
		t.Errorf("service attributes not carried into history: %+v", services[0])
	}

	connections, err := store.History().ConnectionsAt(ctx, observed)
	if err != nil {
		t.Fatalf("reconstructing connections: %v", err)
	}
	if len(connections) != 1 || connections[0].ConnectionID != "10.0.0.1/api->10.0.0.2:5432" {
		t.Fatalf("expected the edge in history at its observation time, got %+v", connections)
	}
}

// TestHistoryOffByDefault guards the opt-in: a TopologyService that was never
// told to record history must not write any, so existing deployments see no
// change in write volume until they enable it.
func TestHistoryOffByDefault(t *testing.T) {
	ts, store := newHistoryTestService(t, false)
	ctx := context.Background()

	if ts.HistoryEnabled() {
		t.Fatal("history should be disabled unless explicitly enabled")
	}

	observed := time.Now().Add(-time.Hour)
	if err := ts.AddOrUpdateService(ctx, &storage.Service{
		ID: "10.0.0.9/quiet", Name: "quiet", LastSeen: observed,
	}); err != nil {
		t.Fatalf("adding service: %v", err)
	}

	got, err := store.History().ServicesAt(ctx, observed)
	if err != nil {
		t.Fatalf("querying history: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no history to be recorded, got %d intervals", len(got))
	}
}

// TestHistorySurvivesCurrentStatePruning is the whole point of the feature.
// Pruning deletes current state after PRUNE_MAX_AGE (30 minutes by default);
// history must outlive it, or "what did this look like last week" is
// unanswerable no matter what else is built on top.
func TestHistorySurvivesCurrentStatePruning(t *testing.T) {
	store, err := sqlite.New(storage.Config{
		Driver:         "sqlite",
		DSN:            ":memory:",
		AutoMigrate:    true,
		HistoryEnabled: true,
		// Retention far longer than the current-state window below.
		HistoryRetention: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	defer store.Close()

	bus := NewEventBus(64)
	defer bus.Close()

	ts := NewTopologyService(store, bus)
	ts.EnableHistory(5 * time.Minute)
	ctx := context.Background()

	observed := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := ts.AddOrUpdateService(ctx, &storage.Service{
		ID: "10.0.0.3/ephemeral", Name: "ephemeral", LastSeen: observed,
	}); err != nil {
		t.Fatalf("adding service: %v", err)
	}

	// Prune current state. A negative maxAge puts the cutoff in the future so
	// every live row goes, deterministically - the storage layer stamps
	// last_seen itself on write and ignores the caller's value, so a realistic
	// 30-minute window would not prune a row written moments ago and the test
	// would be asserting nothing.
	if _, err := store.Prune(ctx, -time.Hour); err != nil {
		t.Fatalf("pruning: %v", err)
	}

	live, err := store.Services().Get(ctx, "10.0.0.3/ephemeral")
	if err != nil {
		t.Fatalf("getting live service: %v", err)
	}
	if live != nil {
		t.Fatal("test premise broken: the service should have been pruned from current state")
	}

	// The history is the point: still answerable after the live row is gone.
	past, err := store.History().ServicesAt(ctx, observed)
	if err != nil {
		t.Fatalf("reconstructing past topology: %v", err)
	}
	if len(past) != 1 || past[0].ServiceID != "10.0.0.3/ephemeral" {
		t.Errorf("history did not survive current-state pruning: got %+v", past)
	}
}
