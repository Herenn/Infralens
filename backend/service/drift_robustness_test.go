package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// A collection outage longer than GoneAfter, with the detector running
// throughout: recovering must not announce every edge not yet re-observed -
// but an edge that then stays unobserved for GoneAfter of observed time is
// still reported.
func TestDriftOutageRecoveryDoesNotFloodGone(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-10*24*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.see("batch", "warehouse", 5439)
	h.advance(2 * time.Hour)
	n := &recordingNotifier{}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)

	// batch->warehouse last ran 4h before the outage; api->db is continuous.
	for i := 0; i < 24; i++ {
		h.see("api", "db", 5432)
		h.advance(5 * time.Minute)
		d.step(ctx)
	}
	for i := 0; i < 3*24*12; i++ { // 3 days, nothing observed at all
		h.advance(5 * time.Minute)
		d.step(ctx)
	}
	h.see("api", "db", 5432) // recovery
	h.advance(5 * time.Minute)
	d.step(ctx)
	if _, gone := n.all(); len(gone) != 0 {
		t.Fatalf("recovering from a 3-day outage reported gone: %v", gone)
	}

	// batch->warehouse had 2h of observed absence before the outage (the
	// first 2h of this test observe nothing at all - a silence too). Another
	// 23h of observation takes it past 24h.
	for i := 0; i < 23*12; i++ {
		h.see("api", "db", 5432)
		h.advance(5 * time.Minute)
		d.step(ctx)
	}
	if _, gone := n.all(); len(gone) != 1 || gone[0] != "batch->warehouse" {
		t.Errorf("after 24h of observed absence: gone = %v, want [batch->warehouse]", gone)
	}
}

// Same, with the backend itself down for 3 days and resuming from its
// checkpoint - both when agents report before the detector seeds and after.
func TestDriftBackendDowntimeDoesNotFloodGone(t *testing.T) {
	for _, seenBeforeSeed := range []bool{false, true} {
		t.Run(fmt.Sprintf("seenBeforeSeed=%v", seenBeforeSeed), func(t *testing.T) {
			h := newDriftHarness(t, time.Now().Add(-10*24*time.Hour))
			ctx := context.Background()
			h.see("api", "db", 5432)
			h.see("batch", "warehouse", 5439)
			h.advance(2 * time.Hour)
			d := h.detectorWith(DriftConfig{Warmup: time.Hour}, nil)
			for i := 0; i < 24; i++ {
				h.see("api", "db", 5432)
				h.advance(5 * time.Minute)
				d.step(ctx)
			}
			h.advance(72 * time.Hour) // backend down
			if seenBeforeSeed {
				h.see("api", "db", 5432)
				h.advance(time.Minute)
			}
			d2 := h.detectorWith(DriftConfig{Warmup: time.Hour}, nil)
			h.see("api", "db", 5432)
			h.advance(5 * time.Minute)
			d2.step(ctx)
			for _, ev := range h.feed() {
				if ev.Kind == storage.DriftDisappeared {
					t.Errorf("reported %s gone after the downtime; it was last seen 4h of observed time earlier", ev.ConnectionID)
				}
			}
		})
	}
}

type flakyDriftRepo struct {
	storage.DriftRepository
	failures     int
	checkpointed int
}

func (f *flakyDriftRepo) InsertEvents(ctx context.Context, evs []storage.DriftEvent) ([]storage.DriftEvent, error) {
	if f.failures > 0 && len(evs) > 0 {
		f.failures--
		return nil, errors.New("database is locked")
	}
	return f.DriftRepository.InsertEvents(ctx, evs)
}

func (f *flakyDriftRepo) SetCheckpoint(ctx context.Context, at time.Time) error {
	f.checkpointed++
	return f.DriftRepository.SetCheckpoint(ctx, at)
}

// A failed insert is retried on later passes rather than lost; the
// checkpoint does not advance past it meanwhile; the webhook hears once.
func TestDriftFailedInsertIsRetried(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	n := &recordingNotifier{}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, n)
	repo := &flakyDriftRepo{DriftRepository: h.store.Drift(), failures: 2}
	d.events = repo

	h.see("api", "cache", 6379)
	h.advance(5 * time.Minute)
	d.step(ctx) // fails: pending
	h.see("api", "db", 5432)
	h.advance(5 * time.Minute)
	d.step(ctx) // retry fails too
	if repo.checkpointed != 0 {
		t.Errorf("checkpoint advanced %d times while a change was unrecorded", repo.checkpointed)
	}
	if len(h.feed()) != 0 {
		t.Fatal("test premise: feed should still be empty")
	}
	h.see("api", "db", 5432)
	h.advance(5 * time.Minute)
	d.step(ctx) // succeeds

	assertFeed(t, h.feed(), "appeared:api->cache")
	if repo.checkpointed != 1 {
		t.Errorf("checkpoint written %d times after the retry succeeded, want 1", repo.checkpointed)
	}
	if appeared, _ := n.all(); len(appeared) != 1 {
		t.Errorf("webhook notified %v, want the change exactly once", appeared)
	}
}

func TestDriftPendingIsBounded(t *testing.T) {
	d := &DriftDetector{}
	batch := make([]storage.DriftEvent, maxPendingDriftEvents/2+1)
	for i := 0; i < 3; i++ {
		d.keepPending(batch)
	}
	if len(d.pending) != maxPendingDriftEvents {
		t.Errorf("pending grew to %d, want capped at %d", len(d.pending), maxPendingDriftEvents)
	}
}

// A checkpoint in the future (clock jumped forward, then back) is reset
// rather than disabling resume until real time catches up.
func TestDriftFutureCheckpointIsReset(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	if err := h.store.Drift().SetCheckpoint(ctx, h.clock.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, nil)
	at, _, _ := h.store.Drift().Checkpoint(ctx)
	if !at.Equal(h.clock) {
		t.Errorf("checkpoint = %s, want reset to now %s", at, h.clock)
	}
	h.advance(5 * time.Minute)
	h.see("api", "db", 5432)
	d.step(ctx)
	at, _, _ = h.store.Drift().Checkpoint(ctx)
	if !at.Equal(h.clock) {
		t.Errorf("checkpoint did not advance after the reset: %s", at)
	}
}

// Edges that can never be reported (inbound from public client IPs - one per
// visitor) are not kept in memory.
func TestDriftDoesNotKeepUnreportableEdges(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	for i := 0; i < 50; i++ {
		h.see(fmt.Sprintf("203.0.113.%d", i), "10.0.0.5/nginx", 443)
	}
	h.see("api", "db", 5432)
	h.advance(time.Hour)
	d := h.detectorWith(DriftConfig{Warmup: 0}, nil)
	if len(d.known) != 1 {
		t.Errorf("known holds %d edges, want only the 1 reportable one", len(d.known))
	}
	h.see("203.0.113.200", "10.0.0.5/nginx", 443)
	h.advance(5 * time.Minute)
	d.step(context.Background())
	if len(d.known) != 1 || len(h.feed()) != 0 {
		t.Errorf("a new visitor was kept (%d known) or reported (%v)", len(d.known), feedIDs(h.feed()))
	}
}

// TestDriftSeedCost measures seeding over a large history. Opt-in:
// INFRALENS_SEED_INTERVALS=N (the reviewer measured 200000).
func TestDriftSeedCost(t *testing.T) {
	n := 0
	if v := os.Getenv("INFRALENS_SEED_INTERVALS"); v != "" {
		_, _ = fmt.Sscan(v, &n)
	}
	if n <= 0 {
		t.Skip("set INFRALENS_SEED_INTERVALS to run")
	}
	for _, inbound := range []bool{true, false} {
		t.Run(fmt.Sprintf("inbound=%v", inbound), func(t *testing.T) {
			store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: t.TempDir() + "/seed.db", AutoMigrate: true, HistoryEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			ctx := context.Background()
			now := time.Now()
			err = store.RunInTransaction(ctx, func(ctx context.Context) error {
				for i := 0; i < n; i++ {
					at := now.Add(-time.Duration(i%(29*24*60)) * time.Minute)
					src := fmt.Sprintf("203.0.%d.%d", i/256%256, i%256) // public client
					if !inbound {
						src = fmt.Sprintf("10.%d.%d.%d/worker", i/65536%256, i/256%256, i%256)
					}
					c := &storage.Connection{ID: src + "->10.0.1.10/nginx:443", SourceID: src, TargetID: "10.0.1.10/nginx", Port: 443}
					if err := store.History().RecordConnection(ctx, c, at, 5*time.Minute); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			bus := NewEventBus(64)
			defer bus.Close()
			ts := NewTopologyService(store, bus)
			ts.EnableHistory(5*time.Minute, storage.DefaultHistoryRetention)
			d, err := ts.NewDriftDetector(DriftConfig{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			var m0, m1, m2 runtime.MemStats
			runtime.ReadMemStats(&m0)
			start := time.Now()
			if err := d.seed(ctx); err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			runtime.ReadMemStats(&m1)
			runtime.GC()
			runtime.ReadMemStats(&m2)
			t.Logf("seed over %d intervals: %s, allocated %.1f MiB, retained %.1f MiB, known=%d",
				n, took, float64(m1.TotalAlloc-m0.TotalAlloc)/(1<<20),
				float64(int64(m2.HeapAlloc)-int64(m0.HeapAlloc))/(1<<20), len(d.known))
			start = time.Now()
			if _, err := d.check(ctx); err != nil {
				t.Fatal(err)
			}
			t.Logf("one steady-state pass: %s", time.Since(start))
			runtime.KeepAlive(d)
		})
	}
}
