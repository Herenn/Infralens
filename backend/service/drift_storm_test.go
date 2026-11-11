package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// A rollout that introduces 5000 new edges in one pass: all recorded, all
// named (through the batched current-state read), in reasonable time.
func TestDriftStorm(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	d := h.detectorWith(DriftConfig{Warmup: time.Hour}, nil)
	sub := h.ts.eventBus.Subscribe("storm")

	const n = 5000
	for i := 0; i < n; i++ {
		src := fmt.Sprintf("10.1.%d.%d/worker", i/250, i%250)
		if err := h.store.Services().Upsert(ctx, &storage.Service{ID: src, Name: fmt.Sprintf("worker-%d", i)}); err != nil {
			t.Fatal(err)
		}
		h.see(src, fmt.Sprintf("10.2.%d.%d:8080", i/250, i%250), 8080)
	}
	h.advance(5 * time.Minute)
	start := time.Now()
	d.step(ctx)
	t.Logf("one pass recording %d appeared edges: %s", n, time.Since(start))

	feed := h.feed()
	if len(feed) != n {
		t.Fatalf("feed has %d events, want %d", len(feed), n)
	}
	for _, ev := range feed {
		if ev.SourceName == "" {
			t.Fatalf("%s recorded without a name", ev.ConnectionID)
		}
	}
	select {
	case ev := <-sub.Events:
		if events, _ := ev.Data.([]storage.DriftEvent); len(events) != n {
			t.Errorf("bus event carries %d events, want %d (capping is the WebSocket's job)", len(events), n)
		}
	default:
		t.Error("no bus event")
	}
}
