package service

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// newImpactTestService builds a TopologyService over an in-memory store,
// with no history recording - GetImpact operates purely on current state.
func newImpactTestService(t *testing.T) *TopologyService {
	t.Helper()

	store, err := sqlite.New(storage.Config{
		Driver:      "sqlite",
		DSN:         ":memory:",
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := NewEventBus(64)
	t.Cleanup(bus.Close)

	return NewTopologyService(store, bus)
}

// addChain wires up services and connections forming src1->src2->...->srcN in
// the TopologyService, using each name as both its ID and Name.
func addChain(t *testing.T, ts *TopologyService, names ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	for _, name := range names {
		if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: name, Name: name, LastSeen: now}); err != nil {
			t.Fatalf("adding service %s: %v", name, err)
		}
	}
	for i := 0; i < len(names)-1; i++ {
		src, dst := names[i], names[i+1]
		if err := ts.AddConnection(ctx, &storage.Connection{
			ID: src + "->" + dst, SourceID: src, TargetID: dst,
			Port: 80, Protocol: "tcp", LastSeen: now,
		}); err != nil {
			t.Fatalf("adding connection %s->%s: %v", src, dst, err)
		}
	}
}

func serviceIDs(topo *storage.Topology) []string {
	ids := make([]string, 0, len(topo.Services))
	for _, svc := range topo.Services {
		ids = append(ids, svc.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestGetImpact_Upstream_TransitiveCallers checks that upstream traversal on
// a chain A->B->C->D finds every transitive caller of the seed, not just the
// direct one - "what breaks if I take C down" must include A, which never
// talks to C directly.
func TestGetImpact_Upstream_TransitiveCallers(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B", "C", "D")

	topo, err := ts.GetImpact(context.Background(), "C", ImpactUpstream, 0)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	if topo == nil {
		t.Fatal("expected a topology, got nil")
	}
	if got := serviceIDs(topo); !equalStrings(got, []string{"A", "B", "C"}) {
		t.Errorf("services = %v, want [A B C]", got)
	}
	if len(topo.Connections) != 2 {
		t.Errorf("connections = %d, want 2 (A->B, B->C)", len(topo.Connections))
	}
}

// TestGetImpact_Downstream_TransitiveDependencies is the mirror check: "what
// does B depend on" must include D, which B never calls directly.
func TestGetImpact_Downstream_TransitiveDependencies(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B", "C", "D")

	topo, err := ts.GetImpact(context.Background(), "B", ImpactDownstream, 0)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	if got := serviceIDs(topo); !equalStrings(got, []string{"B", "C", "D"}) {
		t.Errorf("services = %v, want [B C D]", got)
	}
}

// TestGetImpact_DepthCap checks that a shallow depth stops the traversal
// before it reaches the whole chain, rather than always walking to the end.
func TestGetImpact_DepthCap(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B", "C", "D", "E")

	topo, err := ts.GetImpact(context.Background(), "A", ImpactDownstream, 2)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	// Depth 2 from A: A (seed) -> B (hop 1) -> C (hop 2). D and E must not
	// appear.
	if got := serviceIDs(topo); !equalStrings(got, []string{"A", "B", "C"}) {
		t.Errorf("services = %v, want [A B C]", got)
	}
}

// TestGetImpact_Cycle checks that a mutual dependency doesn't get revisited
// forever: with maxDepth set well beyond what a correct traversal needs, the
// visited set - not the depth cap - is what has to stop this from growing.
func TestGetImpact_Cycle(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"A", "B"} {
		if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: id, Name: id, LastSeen: now}); err != nil {
			t.Fatalf("adding service %s: %v", id, err)
		}
	}
	if err := ts.AddConnection(ctx, &storage.Connection{ID: "A->B", SourceID: "A", TargetID: "B", Port: 80, Protocol: "tcp", LastSeen: now}); err != nil {
		t.Fatalf("adding A->B: %v", err)
	}
	if err := ts.AddConnection(ctx, &storage.Connection{ID: "B->A", SourceID: "B", TargetID: "A", Port: 80, Protocol: "tcp", LastSeen: now}); err != nil {
		t.Fatalf("adding B->A: %v", err)
	}

	topo, err := ts.GetImpact(ctx, "A", ImpactDownstream, maxImpactDepth)
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	if got := serviceIDs(topo); !equalStrings(got, []string{"A", "B"}) {
		t.Errorf("services = %v, want [A B]", got)
	}
	if len(topo.Connections) != 2 {
		t.Errorf("connections = %d, want 2 (A->B, B->A), got duplicates from revisiting: %+v", len(topo.Connections), topo.Connections)
	}
}

// TestGetImpact_UnknownService matches GetService's not-found convention:
// (nil, nil), not an error, so the handler can turn it into a 404.
func TestGetImpact_UnknownService(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B")

	topo, err := ts.GetImpact(context.Background(), "does-not-exist", ImpactUpstream, 0)
	if err != nil {
		t.Fatalf("expected no error for an unknown service, got %v", err)
	}
	if topo != nil {
		t.Errorf("expected nil topology for an unknown service, got %+v", topo)
	}
}

// TestGetCriticality_RanksByBlastRadius checks the ranking order on a chain
// A->B->C->D, where each service's upstream blast radius is exactly the
// count of services before it: D has 3 (A,B,C), C has 2, B has 1, A has 0.
func TestGetCriticality_RanksByBlastRadius(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B", "C", "D")

	ranked, err := ts.GetCriticality(context.Background(), 0)
	if err != nil {
		t.Fatalf("GetCriticality: %v", err)
	}
	if len(ranked) != 4 {
		t.Fatalf("expected 4 ranked services, got %d: %+v", len(ranked), ranked)
	}

	want := map[string]int{"A": 0, "B": 1, "C": 2, "D": 3}
	for _, r := range ranked {
		if r.BlastRadius != want[r.Service.ID] {
			t.Errorf("BlastRadius(%s) = %d, want %d", r.Service.ID, r.BlastRadius, want[r.Service.ID])
		}
	}

	// Descending order: D (3) first, A (0) last.
	if ranked[0].Service.ID != "D" || ranked[len(ranked)-1].Service.ID != "A" {
		var ids []string
		for _, r := range ranked {
			ids = append(ids, r.Service.ID)
		}
		t.Errorf("expected descending order starting with D and ending with A, got %v", ids)
	}
}

// TestGetCriticality_Limit checks that limit caps the result to the
// highest-ranked entries, not an arbitrary subset.
func TestGetCriticality_Limit(t *testing.T) {
	ts := newImpactTestService(t)
	addChain(t, ts, "A", "B", "C", "D")

	ranked, err := ts.GetCriticality(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetCriticality: %v", err)
	}
	if len(ranked) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(ranked))
	}
	if ranked[0].Service.ID != "D" || ranked[1].Service.ID != "C" {
		t.Errorf("expected [D C] (the two highest-ranked), got [%s %s]", ranked[0].Service.ID, ranked[1].Service.ID)
	}
}

// TestGetOrphanServices_FindsOnlyUnconnected checks that a service with no
// connections at all is flagged, and that services with even one edge are
// not.
func TestGetOrphanServices_FindsOnlyUnconnected(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()
	addChain(t, ts, "A", "B") // A->B: neither is an orphan

	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "Z", Name: "isolated", LastSeen: time.Now()}); err != nil {
		t.Fatalf("adding isolated service: %v", err)
	}

	orphans, err := ts.GetOrphanServices(ctx)
	if err != nil {
		t.Fatalf("GetOrphanServices: %v", err)
	}
	if len(orphans) != 1 || orphans[0].ID != "Z" {
		t.Errorf("expected exactly [Z], got %+v", orphans)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
