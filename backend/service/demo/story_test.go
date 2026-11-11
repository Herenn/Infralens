package demo

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// TestDemoStoryInvariants pins what makes the demo one story rather than
// three features' demos side by side: a unique cast, one past that the feed,
// time travel, idle spend and the panels all read the same way, names on
// every external destination, idle candidates that are not drift actors, and
// a restart that changes nothing.
func TestDemoStoryInvariants(t *testing.T) {
	ts := newDemoTopology(t)
	ctx := context.Background()
	ts.ConfigureIdleCost(service.IdleCostConfig{Warmup: -1})

	sim := NewSimulator(ts)
	started := time.Now()
	if err := sim.start(ctx, started); err != nil {
		t.Fatal(err)
	}

	// --- A unique cast: one ID per identity, one identity per address. ---
	everyService := map[string]storage.Service{}
	addService := func(svc storage.Service, where string) {
		if prev, dup := everyService[svc.ID]; dup && (prev.Name != svc.Name || prev.Tech != svc.Tech) {
			t.Errorf("service ID %s used for two identities (%s): %q and %q", svc.ID, where, prev.Name, svc.Name)
		}
		everyService[svc.ID] = svc
	}
	for _, svc := range sim.services {
		addService(svc, "live")
	}
	for _, p := range sim.past {
		if p.service != nil {
			addService(*p.service, "history")
		}
	}
	scriptActors := map[string]bool{}
	for _, step := range sim.script {
		for _, a := range step.actors {
			scriptActors[a] = true
		}
	}
	// The script's new external targets join the cast once applied.
	for _, id := range []string{"198.51.100.40:443", "198.51.100.77:443"} {
		addService(external(strings.TrimSuffix(id, ":443")), "script")
	}
	byIP := map[string]string{}
	for id, svc := range everyService {
		if svc.PodIP == "" {
			t.Errorf("%s has no address", id)
			continue
		}
		if other, ok := byIP[svc.PodIP]; ok && other != id {
			t.Errorf("address %s is used by both %s and %s", svc.PodIP, other, id)
		}
		byIP[svc.PodIP] = id
	}

	// --- Every external destination has a plausible DNS name. ---
	hasName := map[string]bool{}
	for _, p := range sim.past {
		if c := p.connection; c != nil && len(c.hostnames) > 0 {
			hasName[c.targetID] = true
		}
	}
	for _, step := range []string{"198.51.100.40:443", "198.51.100.77:443"} {
		hasName[step] = true // checked live below
	}
	for id := range everyService {
		ip := net.ParseIP(strings.Split(strings.Split(id, "/")[0], ":")[0])
		if ip == nil || ip.IsPrivate() || strings.HasPrefix(id, "203.0.113.") {
			continue // internal, or the inbound client (clients have no name)
		}
		if !hasName[id] {
			t.Errorf("external destination %s has no DNS name", id)
		}
	}

	// --- Idle candidates are exactly the idle cast, and none is a drift actor. ---
	report, err := ts.GetIdleSpend(ctx, service.IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	idle := map[string]bool{}
	for _, it := range report.Items {
		idle[it.ID] = true
		if scriptActors[it.ID] {
			t.Errorf("%s is both an idle-spend candidate and a live drift actor", it.ID)
		}
	}
	for _, id := range []string{idReportsV1, idReportsMySQL, idAdminPanel, idSearchV1, idBatchExporter} {
		if !idle[id] {
			t.Errorf("%s should be an idle-spend candidate", id)
		}
	}
	if len(report.Excluded) == 0 {
		t.Error("the idle-spend panel should show held-back items")
	}

	// --- The feed and time travel agree, and so do feed and idle evidence. ---
	page, err := ts.ListDriftEvents(ctx, storage.DriftEventFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) < 6 {
		t.Fatalf("seeded feed has %d events, want a populated history", len(page.Events))
	}
	gone := map[string]storage.DriftEvent{}
	for _, ev := range page.Events {
		moment := ev.FirstSeen.Add(time.Second)
		if ev.Kind == storage.DriftDisappeared {
			moment = ev.LastSeen
			gone[ev.ConnectionID] = ev
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
			t.Errorf("%s %s: the feed says it was there at %s, time travel does not show it", ev.Kind, ev.ConnectionID, moment)
		}
		if ev.External && (ev.TargetName == "" || net.ParseIP(ev.TargetName) != nil) {
			t.Errorf("%s: internet-bound event names its target %q, want the DNS name", ev.ConnectionID, ev.TargetName)
		}
		if ev.Kind == storage.DriftDisappeared {
			// Gone means gone: a day after the last sighting time travel no
			// longer shows it.
			later, _ := ts.GetTopologyAt(ctx, ev.LastSeen.Add(24*time.Hour))
			for _, c := range later.Connections {
				if c.ID == ev.ConnectionID {
					t.Errorf("%s reported gone but still in the topology a day later", ev.ConnectionID)
				}
			}
		}
	}
	// reports-v1's idle evidence and the feed tell the same story.
	if ev, ok := gone["10.0.1.10/nginx->10.0.4.10/reports-v1:8080"]; !ok {
		t.Error("the feed should record reports-v1 losing its last caller")
	} else {
		for _, it := range report.Items {
			if it.ID == idReportsV1 {
				if it.Evidence.LastInbound == nil || it.Evidence.LastInbound.Sub(ev.LastSeen).Abs() > 10*time.Minute {
					t.Errorf("idle evidence last inbound %v, feed last seen %s", it.Evidence.LastInbound, ev.LastSeen)
				}
			}
		}
	}

	// --- Panels have content: history, orphans, decommission, criticality. ---
	bounds, ok, err := ts.GetHistoryBounds(ctx)
	if err != nil || !ok || bounds.Latest.Sub(bounds.Earliest) < 13*24*time.Hour {
		t.Errorf("history range %+v (ok=%v, err=%v), want about two weeks", bounds, ok, err)
	}
	orphans, _ := ts.GetOrphanServices(ctx)
	if len(orphans) == 0 {
		t.Error("the Orphans panel would be empty")
	}
	stale, _ := ts.GetStaleServices(ctx, 0, 50)
	if len(stale) == 0 {
		t.Error("the Decommission panel would be empty")
	}

	// --- Hostnames on the live map. ---
	topo, err := ts.GetTopology(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range topo.Services {
		if strings.HasPrefix(svc.ID, "198.51.100.") && service.ServiceHostname(&svc) == "" {
			t.Errorf("%s has no hostname on the live map", svc.ID)
		}
	}

	// --- A restart on the kept database changes nothing. ---
	again := NewSimulator(ts)
	if err := again.start(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	page2, _ := ts.ListDriftEvents(ctx, storage.DriftEventFilter{Limit: 100})
	if len(page2.Events) != len(page.Events) {
		t.Errorf("restart changed the feed: %d events, was %d", len(page2.Events), len(page.Events))
	}
	bounds2, _, _ := ts.GetHistoryBounds(ctx)
	if !bounds2.Earliest.Equal(bounds.Earliest) {
		t.Errorf("restart moved the start of history: %s -> %s", bounds.Earliest, bounds2.Earliest)
	}
}

// With drift detection off the demo writes no drift events: there is no
// feed (the endpoints answer 404) to show them in.
func TestDemoWithoutDriftFeed(t *testing.T) {
	ts := newDemoTopology(t)
	ctx := context.Background()
	sim := NewSimulator(ts)
	sim.DisableDriftFeed()
	if err := sim.start(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	page, err := ts.ListDriftEvents(ctx, storage.DriftEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Errorf("seeded %d drift events with drift detection off", len(page.Events))
	}
	if _, ok, _ := ts.GetHistoryBounds(ctx); !ok {
		t.Error("history should still be backfilled")
	}
}

// The demo is the product's shop window (and the README GIF is recorded from
// it): host meters stay in the UI's green band (< 60%) however long it runs,
// with some variation.
func TestDemoNodeMetersStayCalm(t *testing.T) {
	sim := NewSimulator(newDemoTopology(t))
	lo, hi := map[string]float64{}, map[string]float64{}
	for i := 0; i < 5000; i++ {
		for _, n := range sim.nodes {
			sim.wander(n)
			for k, v := range map[string]float64{n.name + " cpu": n.cpuPercent, n.name + " mem": n.memPercent} {
				if v >= 60 {
					t.Fatalf("%s at %.1f%%: amber/red in the demo", k, v)
				}
				if _, ok := lo[k]; !ok || v < lo[k] {
					lo[k] = v
				}
				if v > hi[k] {
					hi[k] = v
				}
			}
		}
	}
	for k := range lo {
		if hi[k]-lo[k] < 1 {
			t.Errorf("%s never moves (%.1f..%.1f)", k, lo[k], hi[k])
		}
	}
}
