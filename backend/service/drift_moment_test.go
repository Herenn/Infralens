package service

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

func topologyHasEdge(t *testing.T, ts *TopologyService, at time.Time, id string) bool {
	t.Helper()
	topo, err := ts.GetTopologyAt(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range topo.Connections {
		if c.ID == id {
			return true
		}
	}
	return false
}

// The Changes panel's "View when it appeared" jumps to first_seen + 1s and
// "View when last seen" to last_seen (frontend/src/lib/drift.ts driftMoment).
// Both must land on a graph that shows the edge - including a short-lived
// edge seen exactly once, where jumping to detected_at (beyond the 5-minute
// grace) would not.
func TestDriftJumpMomentsShowTheEdge(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	hist := h.store.History()
	seeSvc := func(ids ...string) {
		for _, id := range ids {
			if err := hist.RecordService(ctx, &storage.Service{ID: id, Name: id}, h.clock, 5*time.Minute); err != nil {
				t.Fatal(err)
			}
		}
	}

	seeSvc("api", "db")
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	d := h.detectorWith(DriftConfig{Interval: 10 * time.Minute, GoneAfter: 30 * time.Minute, Warmup: time.Hour}, nil)

	// A one-off call, seen once.
	seeSvc("api", "db", "batch")
	h.see("api", "db", 5432)
	h.see("api", "batch", 9000)
	h.advance(7 * time.Minute) // detected one pass later, beyond the grace
	seeSvc("api", "db")
	h.see("api", "db", 5432)
	d.step(ctx)

	var appeared storage.DriftEvent
	for _, ev := range h.feed() {
		if ev.ConnectionID == "api->batch" {
			appeared = ev
		}
	}
	if appeared.ID == 0 {
		t.Fatal("appearance not recorded")
	}
	if topologyHasEdge(t, h.ts, appeared.DetectedAt, "api->batch") {
		t.Log("note: detected_at happens to show the edge here")
	}
	if !topologyHasEdge(t, h.ts, appeared.FirstSeen.Add(time.Second), "api->batch") {
		t.Errorf("topology at first_seen+1s (%s) does not show the appeared edge", appeared.FirstSeen.Add(time.Second))
	}

	// It never comes back: reported gone after GoneAfter of observation.
	for i := 0; i < 8; i++ {
		h.advance(5 * time.Minute)
		seeSvc("api", "db")
		h.see("api", "db", 5432)
		d.step(ctx)
	}
	var gone storage.DriftEvent
	for _, ev := range h.feed() {
		if ev.ConnectionID == "api->batch" && ev.Kind == storage.DriftDisappeared {
			gone = ev
		}
	}
	if gone.ID == 0 {
		t.Fatal("disappearance not recorded")
	}
	if topologyHasEdge(t, h.ts, gone.DetectedAt, "api->batch") {
		t.Error("test premise: detected_at of a disappearance should not show the edge")
	}
	if !topologyHasEdge(t, h.ts, gone.LastSeen, "api->batch") {
		t.Errorf("topology at last_seen (%s) does not show the disappeared edge", gone.LastSeen)
	}
}
