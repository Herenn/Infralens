package demo

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// TestDemoPopulatesIdleSpend pins the out-of-the-box demo story: the panel
// must have believable content with no configuration at all, and the guards
// must visibly hold back the worker.
func TestDemoPopulatesIdleSpend(t *testing.T) {
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bus := service.NewEventBus(256)
	t.Cleanup(func() { bus.Close(); _ = store.Close() })

	ts := service.NewTopologyService(store, bus)
	ts.EnableHistory(5*time.Minute, storage.DefaultHistoryRetention)
	ts.ConfigureIdleCost(service.IdleCostConfig{Warmup: -1}) // what config.Load yields with nothing set

	sim := NewSimulator(ts)
	if err := sim.seed(context.Background()); err != nil {
		t.Fatal(err)
	}

	r, err := ts.GetIdleSpend(context.Background(), service.IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pricing.Source != "demo" {
		t.Errorf("pricing source = %q, want demo", r.Pricing.Source)
	}

	want := map[string]string{
		"10.0.4.10/reports-v1":     service.IdleCategoryQuiet,
		"10.0.4.11:3306":           service.IdleCategoryIdleCallersOnly,
		"10.0.2.17/admin-panel":    service.IdleCategoryQuiet,
		"10.0.3.17:9200":           service.IdleCategoryOrphan,
		"10.0.2.16/batch-exporter": service.IdleCategoryStale,
	}
	got := map[string]string{}
	for _, it := range r.Items {
		got[it.ID] = it.Category
		if it.Cost == nil {
			t.Errorf("%s has no cost in demo mode", it.ID)
		}
	}
	for id, cat := range want {
		if got[id] != cat {
			t.Errorf("%s: category = %q, want %q", id, got[id], cat)
		}
	}
	if len(r.Items) != len(want) {
		t.Errorf("items = %v, want exactly %v", got, want)
	}

	excludedWorker := false
	for _, e := range r.Excluded {
		if e.ID == "10.0.2.15/nightly-etl" && e.Reason == service.IdleExcludedOutboundOnly {
			excludedWorker = true
		}
	}
	if !excludedWorker {
		t.Errorf("nightly-etl should be held back as an outbound-only worker; excluded = %+v", r.Excluded)
	}
	if r.Totals.Monthly < 200 || r.Totals.Monthly > 400 {
		t.Errorf("demo total = %v, expected a few hundred", r.Totals.Monthly)
	}
	t.Logf("demo idle spend: $%.2f/mo across %d services, by confidence %v", r.Totals.Monthly, r.Totals.Services, r.Totals.MonthlyByConfidence)
}

// TestDemoBackfillIsStableAcrossRestarts: on a persisted store, a restarted
// demo must not rewrite the past (review item 17).
func TestDemoBackfillIsStableAcrossRestarts(t *testing.T) {
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bus := service.NewEventBus(256)
	t.Cleanup(func() { bus.Close(); _ = store.Close() })
	ts := service.NewTopologyService(store, bus)
	ts.EnableHistory(5*time.Minute, storage.DefaultHistoryRetention)
	ctx := context.Background()

	pastEnds := func() map[string]time.Time {
		rows, err := store.History().ConnectionsBetween(ctx, time.Now().Add(-30*24*time.Hour), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]time.Time{}
		counts := map[string]int{}
		for _, r := range rows {
			counts[r.ConnectionID]++
			if r.LastSeen.After(out[r.ConnectionID]) {
				out[r.ConnectionID] = r.LastSeen
			}
		}
		for id, n := range counts {
			if n > 1 {
				t.Errorf("%s has %d intervals after restart", id, n)
			}
		}
		return out
	}

	if err := NewSimulator(ts).seed(ctx); err != nil {
		t.Fatal(err)
	}
	first := pastEnds()
	time.Sleep(20 * time.Millisecond)
	if err := NewSimulator(ts).seed(ctx); err != nil { // restart
		t.Fatal(err)
	}
	second := pastEnds()
	for _, id := range []string{
		"10.0.1.10/nginx->10.0.4.10/reports-v1:8080",
		"10.0.2.10/api-gateway->10.0.2.17/admin-panel:3000",
		"10.0.2.16/batch-exporter->10.0.3.12:9092:9092",
	} {
		if first[id].IsZero() || !first[id].Equal(second[id]) {
			t.Errorf("%s ended %v, after restart %v", id, first[id], second[id])
		}
	}
}
