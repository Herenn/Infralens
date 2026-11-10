package demo

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

func newDemoTopology(t *testing.T) *service.TopologyService {
	t.Helper()
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bus := service.NewEventBus(1024)
	t.Cleanup(bus.Close)
	ts := service.NewTopologyService(store, bus)
	ts.EnableHistory(storage.DefaultHistoryMaxGap, storage.DefaultHistoryRetention)
	return ts
}

// TestSeededPastIsConsistent: every seeded event refers to an edge that the
// backfilled history shows at the event's "view this moment" instant, and a
// restart against the same database does not seed again.
func TestSeededPastIsConsistent(t *testing.T) {
	ts := newDemoTopology(t)
	ctx := context.Background()

	sim := NewSimulator(ts)
	sim.started = time.Now()
	began := time.Now()
	sim.seedPast(ctx)
	t.Logf("backfill took %s", time.Since(began))

	page, err := ts.ListDriftEvents(ctx, storage.DriftEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]storage.DriftEventKind{}
	for _, ev := range page.Events {
		kinds[ev.ConnectionID] = ev.Kind
		if ev.DetectedAt.After(time.Now()) {
			t.Errorf("%s detected in the future", ev.ConnectionID)
		}
		moment := ev.DetectedAt
		if ev.Kind == storage.DriftDisappeared {
			moment = ev.LastSeen
		}
		topo, err := ts.GetTopologyAt(ctx, moment)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range topo.Connections {
			found = found || c.ID == ev.ConnectionID
		}
		if !found {
			t.Errorf("%s %s: edge not in the topology at %s", ev.Kind, ev.ConnectionID, moment)
		}
		if ev.SourceName == "" || ev.TargetName == "" {
			t.Errorf("%s: missing names %q -> %q", ev.ConnectionID, ev.SourceName, ev.TargetName)
		}
	}
	want := map[string]storage.DriftEventKind{
		"10.0.2.16/batch-exporter->10.0.3.12:9092:9092":         storage.DriftDisappeared,
		"10.0.1.10/nginx->10.0.4.10/reports-v1:8080":            storage.DriftDisappeared,
		"10.0.2.10/api-gateway->10.0.2.17/admin-panel:3000":     storage.DriftDisappeared,
		"10.0.2.12/orders-service->10.0.2.20/inventory-v1:8081": storage.DriftDisappeared,
		"10.0.2.13/payment-worker->10.0.3.16:5672:5672":         storage.DriftDisappeared,
		"10.0.2.13/payment-worker->198.51.100.20:443:443":       storage.DriftAppeared,
		"10.0.2.12/orders-service->10.0.3.15:9200:9200":         storage.DriftAppeared,
		"10.0.2.11/auth-service->198.51.100.61:443:443":         storage.DriftAppeared,
	}
	if len(kinds) != len(want) {
		t.Errorf("seeded %v, want %v", kinds, want)
	}
	for id, kind := range want {
		if kinds[id] != kind {
			t.Errorf("%s: got %q, want %q", id, kinds[id], kind)
		}
	}

	// A restart against the same database leaves the past alone...
	again := NewSimulator(ts)
	again.started = time.Now()
	if kept := again.seedPast(ctx); !kept {
		t.Error("restart did not notice the kept database")
	}
	page2, _ := ts.ListDriftEvents(ctx, storage.DriftEventFilter{})
	if len(page2.Events) != len(page.Events) {
		t.Errorf("restart re-seeded: %d events, was %d", len(page2.Events), len(page.Events))
	}

	// ...and resumes after the script rather than replaying it: the retired
	// service must not come back.
	again.fastForwardScript(ctx)
	if err := again.seed(ctx); err != nil {
		t.Fatal(err)
	}
	if svc, _ := ts.GetService(ctx, "10.0.2.14/recommendations"); svc != nil {
		t.Error("retired service came back after a restart")
	}
	if svc, _ := ts.GetService(ctx, "198.51.100.40:443"); svc == nil {
		t.Error("service added by the script is missing after a restart")
	}
}

// TestScriptedDriftIsDetected runs the real detector over the demo: the
// backfilled past and the starting topology produce nothing, and each
// scripted appearance is reported.
func TestScriptedDriftIsDetected(t *testing.T) {
	ts := newDemoTopology(t)
	ctx := context.Background()

	// Demo timings, except a faster check so the test does not wait 15s.
	cfg := TuneDriftConfig(service.DriftConfig{Interval: 300 * time.Millisecond, Warmup: -1})
	detector, err := ts.NewDriftDetector(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go detector.Run(runCtx)

	sim := NewSimulator(ts)
	sim.started = time.Now()
	sim.seedPast(ctx)
	if err := sim.seed(ctx); err != nil {
		t.Fatal(err)
	}
	seeded, _ := ts.ListDriftEvents(ctx, storage.DriftEventFilter{})

	// Fast-forward the script.
	sim.started = sim.started.Add(-10 * time.Minute)
	sim.advanceScript(ctx)

	deadline := time.Now().Add(10 * time.Second)
	want := map[string]bool{
		"10.0.2.12/orders-service->198.51.100.40:443:443": true,
		"10.0.2.11/auth-service->10.0.3.12:9092:9092":     true,
		"10.0.2.10/api-gateway->198.51.100.77:443:443":    true,
	}
	for time.Now().Before(deadline) {
		page, _ := ts.ListDriftEvents(ctx, storage.DriftEventFilter{Kind: storage.DriftAppeared})
		got := 0
		for _, ev := range page.Events {
			if want[ev.ConnectionID] {
				got++
			}
		}
		if got == len(want) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	all, _ := ts.ListDriftEvents(ctx, storage.DriftEventFilter{})
	live := all.Events[:len(all.Events)-len(seeded.Events)]
	if len(live) != len(want) {
		var ids []string
		for _, ev := range live {
			ids = append(ids, string(ev.Kind)+":"+ev.ConnectionID)
		}
		t.Fatalf("detector reported %v, want exactly the 3 scripted appearances", ids)
	}
	for _, ev := range live {
		if !want[ev.ConnectionID] {
			t.Errorf("unexpected event %s %s", ev.Kind, ev.ConnectionID)
		}
	}
}
