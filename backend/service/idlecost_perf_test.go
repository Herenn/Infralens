package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// seedLargeHistory builds an on-disk SQLite store with nSvc services over 50
// nodes, nEdge live connections and perEdge history intervals per edge over
// 14 days - the shape of a busy cluster's history (the review measured 606k
// intervals at ~2s per uncached request).
func seedLargeHistory(tb testing.TB, nSvc, nEdge, perEdge int) (*TopologyService, storage.Store) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "perf.db")
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: path, AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	bus := NewEventBus(64)
	tb.Cleanup(bus.Close)
	ts := NewTopologyService(store, bus)
	ts.EnableHistory(5*time.Minute, storage.DefaultHistoryRetention)
	ctx := context.Background()

	svcID := func(i int) string { return fmt.Sprintf("10.%d.%d.%d/app%d", i/65536, (i/256)%256, i%256, i) }
	for i := 0; i < nSvc; i++ {
		if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: svcID(i), Name: fmt.Sprintf("app%d", i), Type: "web_server", Node: fmt.Sprintf("node-%d", i%50)}); err != nil {
			tb.Fatal(err)
		}
	}
	for e := 0; e < nEdge; e++ {
		c := &storage.Connection{ID: fmt.Sprintf("c%d", e), SourceID: svcID(e % nSvc), TargetID: svcID((e*7 + 1) % nSvc), Port: 80, LastSeen: time.Now()}
		if err := ts.AddConnection(ctx, c); err != nil {
			tb.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		tb.Fatal(err)
	}
	defer db.Close()
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO connection_intervals(connection_id, source_id, target_id, port, protocol, first_seen, last_seen) VALUES (?,?,?,?,?,?,?)`)
	now := time.Now()
	step := 14 * 24 * time.Hour / time.Duration(perEdge)
	for e := 0; e < nEdge; e++ {
		for k := 0; k < perEdge; k++ {
			fs := now.Add(-14*24*time.Hour + time.Duration(k)*step)
			if _, err := stmt.Exec(fmt.Sprintf("c%d", e), svcID(e%nSvc), svcID((e*7+1)%nSvc), 80, "tcp", fs, fs.Add(time.Minute)); err != nil {
				tb.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 100})
	return ts, store
}

// TestIdleSpendLargeHistoryBudget is the performance budget: a cold report
// over 120k history intervals must stay well inside the 15s write timeout,
// and a cached one must be effectively free. Set IDLE_PERF_FULL=1 for the
// review's full size (3000 services, 606k intervals).
func TestIdleSpendLargeHistoryBudget(t *testing.T) {
	nSvc, nEdge, perEdge, budget := 1000, 2000, 60, 1500*time.Millisecond
	if os.Getenv("IDLE_PERF_FULL") != "" {
		nSvc, nEdge, perEdge, budget = 3000, 6000, 100, 3*time.Second
	}
	if testing.Short() || raceEnabled {
		t.Skip("timing budget; seeds a large store")
	}
	ts, store := seedLargeHistory(t, nSvc, nEdge, perEdge)
	ctx := context.Background()

	start := time.Now()
	r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	start = time.Now()
	for i := 0; i < 50; i++ {
		if _, err := ts.GetIdleSpend(ctx, IdleSpendQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	cached := time.Since(start) / 50
	t.Logf("services=%d intervals=%d: cold %v, cached %v, items=%d", nSvc, nEdge*(perEdge+1), cold, cached, r.Totals.Services)
	if cold > budget {
		t.Errorf("cold report took %v, budget %v", cold, budget)
	}
	if cached > 5*time.Millisecond {
		t.Errorf("cached report took %v", cached)
	}

	// The SQL aggregation must agree with the generic fallback.
	now := time.Now()
	from := now.Add(-14 * 24 * time.Hour)
	fast, err := latestConnectionIntervals(ctx, store.History(), from, now)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := latestConnectionIntervals(ctx, historyOnly{store.History()}, from, now)
	if err != nil {
		t.Fatal(err)
	}
	key := func(xs []storage.ConnectionInterval) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = fmt.Sprintf("%s|%d", x.ConnectionID, x.ID)
		}
		sort.Strings(out)
		return out
	}
	a, b := key(fast), key(slow)
	if len(a) != len(b) || len(a) != nEdge {
		t.Fatalf("aggregated %d rows, fallback %d, edges %d", len(a), len(b), nEdge)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d: %s vs %s", i, a[i], b[i])
		}
	}
}

// historyOnly hides the optional aggregation so the fallback path runs.
type historyOnly struct{ storage.HistoryRepository }
