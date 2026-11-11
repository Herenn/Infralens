package service

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// TestDriftSingleNodeAgentOutageIsNotGone is the review's live repro: only
// node-a's agent stops reporting, for longer than GoneAfter. Its edges must
// not be reported gone (that used to produce permanent "disappeared" events
// while Idle spend held the same services back as "not observing"), nothing
// is reported when node-a comes back, and a real disappearance on node-a is
// still reported once its agent is reporting again.
func TestDriftSingleNodeAgentOutageIsNotGone(t *testing.T) {
	for _, heartbeats := range []bool{true, false} {
		name := "with agent heartbeats"
		if !heartbeats {
			name = "history from before heartbeats"
		}
		t.Run(name, func(t *testing.T) {
			h := newDriftHarness(t, time.Now().Add(-48*time.Hour))
			ctx := context.Background()
			const (
				api    = "10.0.1.10/api"
				db     = "10.0.3.10:5432"
				stripe = "198.51.100.20:443"
				web    = "10.0.2.10/web"
				redis  = "10.0.2.20:6379"
			)
			for _, s := range []storage.Service{{ID: api, Name: "api", Node: "node-a"}, {ID: web, Name: "web", Node: "node-b"}} {
				s := s
				if err := h.store.Services().Upsert(ctx, &s); err != nil {
					t.Fatal(err)
				}
			}
			beat := func(node string) {
				if heartbeats {
					if err := h.store.History().RecordNode(ctx, node, h.clock, 5*time.Minute); err != nil {
						t.Fatal(err)
					}
				}
			}
			nodeA := func() { h.see(api, db, 5432); h.see(api, stripe, 443); beat("node-a") }
			nodeB := func() { h.see(web, redis, 6379); beat("node-b") }

			// Two quiet hours of normal operation, then detection starts.
			for i := 0; i < 120; i += 2 {
				nodeA()
				nodeB()
				h.advance(2 * time.Minute)
			}
			d := h.detectorWith(DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}, nil)

			// node-a's agent is down for 3 hours (6x GoneAfter); node-b is fine.
			for i := 0; i < 180; i += 2 {
				nodeB()
				h.advance(2 * time.Minute)
				if i%10 == 0 {
					d.step(ctx)
				}
			}
			if ev := h.feed(); len(ev) != 0 {
				t.Fatalf("node-a's agent outage reported as drift: %v", feedIDs(ev))
			}

			// node-a comes back.
			for i := 0; i < 20; i += 2 {
				nodeA()
				nodeB()
				h.advance(2 * time.Minute)
				d.step(ctx)
			}
			if ev := h.feed(); len(ev) != 0 {
				t.Fatalf("recovery reported as drift: %v", feedIDs(ev))
			}

			// A real disappearance on node-a, with its agent reporting.
			for i := 0; i < 60; i += 2 {
				h.see(api, stripe, 443)
				beat("node-a")
				nodeB()
				h.advance(2 * time.Minute)
				d.step(ctx)
			}
			assertFeed(t, h.feed(), "disappeared:"+api+"->"+db)
		})
	}
}
