package service

import (
	"context"
	"errors"
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

// TestGetTopologyAtRequiresHistoryEnabled guards the same "opt-in" contract
// as TestHistoryOffByDefault, but for the read side: querying a past instant
// without history enabled must fail clearly rather than silently return an
// empty topology, which would look identical to "nothing existed then".
func TestGetTopologyAtRequiresHistoryEnabled(t *testing.T) {
	ts, _ := newHistoryTestService(t, false)
	ctx := context.Background()

	if _, err := ts.GetTopologyAt(ctx, time.Now()); !errors.Is(err, ErrHistoryDisabled) {
		t.Errorf("expected ErrHistoryDisabled, got %v", err)
	}
	if _, _, err := ts.GetHistoryBounds(ctx); !errors.Is(err, ErrHistoryDisabled) {
		t.Errorf("expected ErrHistoryDisabled, got %v", err)
	}
}

// TestGetTopologyAtReconstructsPastShape checks that GetTopologyAt reflects
// what was present at the queried instant, not current state - the same
// distinction TestHistorySurvivesCurrentStatePruning makes for the raw
// interval query, but through the method the HTTP handler actually calls.
func TestGetTopologyAtReconstructsPastShape(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := ts.AddOrUpdateService(ctx, &storage.Service{
		ID: "10.0.0.4/db", Name: "db", Type: "database", LastSeen: past,
	}); err != nil {
		t.Fatalf("adding service: %v", err)
	}
	if err := ts.AddConnection(ctx, &storage.Connection{
		ID: "10.0.0.4/api->10.0.0.4/db:5432", SourceID: "10.0.0.4/api",
		TargetID: "10.0.0.4/db", Port: 5432, Protocol: "tcp", LastSeen: past,
	}); err != nil {
		t.Fatalf("adding connection: %v", err)
	}

	topo, err := ts.GetTopologyAt(ctx, past)
	if err != nil {
		t.Fatalf("GetTopologyAt: %v", err)
	}
	if len(topo.Services) != 1 || topo.Services[0].ID != "10.0.0.4/db" {
		t.Errorf("expected the one service present at %s, got %+v", past, topo.Services)
	}
	if len(topo.Connections) != 1 || topo.Connections[0].ID != "10.0.0.4/api->10.0.0.4/db:5432" {
		t.Errorf("expected the one connection present at %s, got %+v", past, topo.Connections)
	}

	// A service added now must not appear in a topology reconstructed for the
	// past - otherwise this is just GetTopology with extra steps.
	if err := ts.AddOrUpdateService(ctx, &storage.Service{
		ID: "10.0.0.5/new", Name: "new", LastSeen: time.Now(),
	}); err != nil {
		t.Fatalf("adding new service: %v", err)
	}
	topo, err = ts.GetTopologyAt(ctx, past)
	if err != nil {
		t.Fatalf("GetTopologyAt (re-query): %v", err)
	}
	if len(topo.Services) != 1 {
		t.Errorf("a service added after %s leaked into the reconstruction: got %+v", past, topo.Services)
	}
}

// TestGetHistoryBoundsReflectsRecordedRange checks the bounds a UI timeline
// control would use to size itself, through the service method the HTTP
// handler calls rather than the storage layer directly.
func TestGetHistoryBoundsReflectsRecordedRange(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()

	if _, ok, err := ts.GetHistoryBounds(ctx); err != nil {
		t.Fatalf("GetHistoryBounds: %v", err)
	} else if ok {
		t.Error("expected ok=false before anything has been recorded")
	}

	early := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	late := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "10.0.0.6/a", Name: "a", LastSeen: early}); err != nil {
		t.Fatalf("adding early service: %v", err)
	}
	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "10.0.0.6/b", Name: "b", LastSeen: late}); err != nil {
		t.Fatalf("adding late service: %v", err)
	}

	bounds, ok, err := ts.GetHistoryBounds(ctx)
	if err != nil {
		t.Fatalf("GetHistoryBounds: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true once history has been recorded")
	}
	if !bounds.Earliest.Equal(early) {
		t.Errorf("earliest = %s, want %s", bounds.Earliest, early)
	}
	if !bounds.Latest.Equal(late) {
		t.Errorf("latest = %s, want %s", bounds.Latest, late)
	}
}

// TestGetStaleServicesRequiresHistoryEnabled matches the read-side opt-in
// contract every other history query follows.
func TestGetStaleServicesRequiresHistoryEnabled(t *testing.T) {
	ts, _ := newHistoryTestService(t, false)

	if _, err := ts.GetStaleServices(context.Background(), time.Hour); !errors.Is(err, ErrHistoryDisabled) {
		t.Errorf("expected ErrHistoryDisabled, got %v", err)
	}
}

// TestGetStaleServicesOnlyReturnsWhatsOlderThanCutoff checks the boundary
// through the service method the HTTP handler actually calls, not just the
// storage layer directly.
func TestGetStaleServicesOnlyReturnsWhatsOlderThanCutoff(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()

	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	recent := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "10.0.0.7/decommission-me", Name: "old", LastSeen: old}); err != nil {
		t.Fatalf("adding old service: %v", err)
	}
	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "10.0.0.7/keep-me", Name: "recent", LastSeen: recent}); err != nil {
		t.Fatalf("adding recent service: %v", err)
	}

	stale, err := ts.GetStaleServices(ctx, 48*time.Hour)
	if err != nil {
		t.Fatalf("GetStaleServices: %v", err)
	}

	var ids []string
	for _, si := range stale {
		ids = append(ids, si.ServiceID)
	}
	foundOld, foundRecent := false, false
	for _, id := range ids {
		if id == "10.0.0.7/decommission-me" {
			foundOld = true
		}
		if id == "10.0.0.7/keep-me" {
			foundRecent = true
		}
	}
	if !foundOld {
		t.Errorf("expected the service last seen 72h ago to be stale at a 48h cutoff, got %v", ids)
	}
	if foundRecent {
		t.Errorf("the service last seen 1h ago should not be stale at a 48h cutoff, got %v", ids)
	}
}
