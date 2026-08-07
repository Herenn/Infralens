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

	topo, err := ts.GetImpact(context.Background(), "C", ImpactUpstream, 0, time.Time{})
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

	topo, err := ts.GetImpact(context.Background(), "B", ImpactDownstream, 0, time.Time{})
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

	topo, err := ts.GetImpact(context.Background(), "A", ImpactDownstream, 2, time.Time{})
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

	topo, err := ts.GetImpact(ctx, "A", ImpactDownstream, maxImpactDepth, time.Time{})
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

	topo, err := ts.GetImpact(context.Background(), "does-not-exist", ImpactUpstream, 0, time.Time{})
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

// TestGetImpact_OmitsEdgesToMissingEndpoints: services absent from the
// current topology are already dropped from the response, so the edges that
// reach them must go too - otherwise the returned graph references nodes it
// does not contain.
func TestGetImpact_OmitsEdgesToMissingEndpoints(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()
	now := time.Now()

	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "target", Name: "db", LastSeen: now}); err != nil {
		t.Fatalf("adding service: %v", err)
	}
	if err := ts.AddConnection(ctx, &storage.Connection{
		ID: "ghost->target", SourceID: "ghost", TargetID: "target",
		Port: 5432, Protocol: "tcp", LastSeen: now,
	}); err != nil {
		t.Fatalf("adding connection: %v", err)
	}

	topo, err := ts.GetImpact(ctx, "target", ImpactUpstream, 5, time.Time{})
	if err != nil {
		t.Fatalf("GetImpact: %v", err)
	}
	if got := serviceIDs(topo); !equalStrings(got, []string{"target"}) {
		t.Errorf("services = %v, want [target]", got)
	}
	if len(topo.Connections) != 0 {
		t.Errorf("connections = %+v, want none: the only edge points at a service not in the response",
			topo.Connections)
	}
}

// TestGetOrphanServices_CountsPhantomEdgesAsIsolated pins the orphans panel to
// what the graph actually draws.
//
// The graph renders a node per service and an edge per connection, so an edge
// whose other endpoint has no service row draws nothing. A service holding
// only such an edge appears isolated on screen; it used to be excluded from
// the list that claims to enumerate exactly that.
func TestGetOrphanServices_CountsPhantomEdgesAsIsolated(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"truly-alone", "looks-connected"} {
		if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: id, Name: id, LastSeen: now}); err != nil {
			t.Fatalf("adding %s: %v", id, err)
		}
	}
	// Its only peer has no service row.
	if err := ts.AddConnection(ctx, &storage.Connection{
		ID: "looks-connected->ghost", SourceID: "looks-connected", TargetID: "ghost",
		Port: 80, Protocol: "tcp", LastSeen: now,
	}); err != nil {
		t.Fatalf("adding phantom edge: %v", err)
	}
	// A real pair, to prove genuinely connected services are still excluded.
	addChain(t, ts, "web", "db")

	orphans, err := ts.GetOrphanServices(ctx)
	if err != nil {
		t.Fatalf("GetOrphanServices: %v", err)
	}
	got := make([]string, 0, len(orphans))
	for _, o := range orphans {
		got = append(got, o.ID)
	}
	sort.Strings(got)
	if !equalStrings(got, []string{"looks-connected", "truly-alone"}) {
		t.Errorf("orphans = %v, want [looks-connected truly-alone]: a service whose only "+
			"edge points at a service that does not exist renders isolated", got)
	}
}

// TestGetCriticality_IgnoresPhantomEndpoints is the regression guard for a
// blast radius counting services that don't exist.
//
// A connection can outlive its endpoints: UpdateStats refreshes a
// connection's last_seen from throughput reports, while only a connect/accept
// event refreshes a service's, so a busy long-lived connection keeps itself
// alive while its endpoints age out and are pruned. Counting those phantoms
// inflated the ranking - and could report a blast radius larger than the
// total number of services.
func TestGetCriticality_IgnoresPhantomEndpoints(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()
	now := time.Now()

	if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: "real-target", Name: "db", LastSeen: now}); err != nil {
		t.Fatalf("adding service: %v", err)
	}
	for _, ghost := range []string{"ghost-1", "ghost-2", "ghost-3"} {
		if err := ts.AddConnection(ctx, &storage.Connection{
			ID: ghost + "->real-target", SourceID: ghost, TargetID: "real-target",
			Port: 5432, Protocol: "tcp", LastSeen: now,
		}); err != nil {
			t.Fatalf("adding connection from %s: %v", ghost, err)
		}
	}

	ranked, err := ts.GetCriticality(ctx, 10)
	if err != nil {
		t.Fatalf("GetCriticality: %v", err)
	}
	if len(ranked) != 1 {
		t.Fatalf("expected 1 ranked service, got %d", len(ranked))
	}
	if ranked[0].BlastRadius != 0 {
		t.Errorf("blast radius = %d, want 0: none of the three callers exist as services",
			ranked[0].BlastRadius)
	}
}

// TestGetImpact_AtPastInstant guards the same class of bug as the graph
// export: a blast radius requested while viewing a past moment must be
// traversed over that graph, not over current state. Without it the drawer
// answered "what breaks if this goes down" from today's edges and highlighted
// the result onto the historical topology being displayed.
func TestGetImpact_AtPastInstant(t *testing.T) {
	store, err := sqlite.New(storage.Config{
		Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true,
	})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	bus := NewEventBus(64)
	t.Cleanup(bus.Close)
	ts := NewTopologyService(store, bus)
	ts.EnableHistory(storage.DefaultHistoryMaxGap, storage.DefaultHistoryRetention)
	ctx := context.Background()

	// Past: old-caller -> shared. Recorded straight into history so the edge
	// exists then and not now.
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	for _, id := range []string{"old-caller", "shared"} {
		if err := store.History().RecordService(ctx, &storage.Service{ID: id, Name: id},
			past, storage.DefaultHistoryMaxGap); err != nil {
			t.Fatalf("recording %s: %v", id, err)
		}
	}
	if err := store.History().RecordConnection(ctx, &storage.Connection{
		ID: "old-caller->shared", SourceID: "old-caller", TargetID: "shared",
		Port: 80, Protocol: "tcp",
	}, past, storage.DefaultHistoryMaxGap); err != nil {
		t.Fatalf("recording past edge: %v", err)
	}

	// Now: a different caller entirely.
	addChain(t, ts, "new-caller", "shared")

	// Current state: only new-caller calls shared.
	now, err := ts.GetImpact(ctx, "shared", ImpactUpstream, 5, time.Time{})
	if err != nil {
		t.Fatalf("GetImpact (current): %v", err)
	}
	if got := serviceIDs(now); !equalStrings(got, []string{"new-caller", "shared"}) {
		t.Errorf("current impact = %v, want [new-caller shared]", got)
	}

	// At the past instant: only old-caller did.
	then, err := ts.GetImpact(ctx, "shared", ImpactUpstream, 5, past)
	if err != nil {
		t.Fatalf("GetImpact (past): %v", err)
	}
	if got := serviceIDs(then); !equalStrings(got, []string{"old-caller", "shared"}) {
		t.Errorf("impact at %s = %v, want [old-caller shared] - it was answered from current state", past, got)
	}
}

// TestCriticalityAndImpactAgree pins the invariant a user can check by eye:
// the "Most critical" panel reports a blast radius, and clicking through
// highlights a set. Those must be the same number.
//
// They are computed by different calls with different depth defaults -
// GetCriticality always uses maxImpactDepth, GetImpact defaults to 5 - so on a
// chain deeper than 5 the panel said "breaks 9 services" while the impact view
// highlighted 5. The UI now requests maxImpactDepth explicitly; this guards the
// agreement in both the deep case and with a phantom endpoint present.
func TestCriticalityAndImpactAgree(t *testing.T) {
	ts := newImpactTestService(t)
	ctx := context.Background()

	addChain(t, ts, "S0", "S1", "S2", "S3", "S4", "S5", "S6", "S7", "S8", "S9")
	// A caller with no service row must count for neither.
	if err := ts.AddConnection(ctx, &storage.Connection{
		ID: "ghost->S9", SourceID: "ghost", TargetID: "S9",
		Port: 80, Protocol: "tcp", LastSeen: time.Now(),
	}); err != nil {
		t.Fatalf("adding phantom edge: %v", err)
	}

	ranked, err := ts.GetCriticality(ctx, 50)
	if err != nil {
		t.Fatalf("GetCriticality: %v", err)
	}
	if len(ranked) == 0 {
		t.Fatal("expected a ranking")
	}

	for _, sc := range ranked {
		imp, err := ts.GetImpact(ctx, sc.Service.ID, ImpactUpstream, maxImpactDepth, time.Time{})
		if err != nil {
			t.Fatalf("GetImpact(%s): %v", sc.Service.ID, err)
		}
		highlighted := len(imp.Services) - 1 // the impact response includes the seed
		if highlighted != sc.BlastRadius {
			t.Errorf("%s: criticality reports blast radius %d but the impact view highlights %d",
				sc.Service.ID, sc.BlastRadius, highlighted)
		}
	}

	// And the deep end is genuinely deep, so this is not passing on a short graph.
	for _, sc := range ranked {
		if sc.Service.ID == "S9" && sc.BlastRadius != 9 {
			t.Errorf("S9 blast radius = %d, want 9 (the whole chain, phantom excluded)", sc.BlastRadius)
		}
	}
}
