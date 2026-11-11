package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// outageFixture is the review's repro: node n1's agent is dead for 8 days
// (and back for the last hour), while a client on n2 - whose agent is fine -
// keeps calling api on n1 the whole time. billing on n1 was only ever called
// by gw, also on n1, until n1's agent died. Nothing observed billing being
// idle: the only agent that could see its traffic was down.
type outageFixture struct {
	ts                  *TopologyService
	now                 time.Time
	api, billing, gw    string
	client              string
	clientToAPI, gwBill string
}

// newOutageFixture builds the scenario with the given IDs. withHeartbeats
// records agent heartbeats (what every v3.1 agent sends); without them only
// pre-heartbeat (v3.0-era) evidence exists.
func newOutageFixture(t *testing.T, api, billing, gw, client, apiAsSeenByClient string, withHeartbeats bool) *outageFixture {
	t.Helper()
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()
	now := time.Now()
	f := &outageFixture{ts: ts, now: now, api: api, billing: billing, gw: gw, client: client}

	svc := func(id, name, node string) *storage.Service {
		return &storage.Service{ID: id, Name: name, Type: "web_server", Node: node}
	}
	n1 := []*storage.Service{svc(api, "api", "n1"), svc(billing, "billing", "n1"), svc(gw, "gw", "n1")}
	cl := svc(client, "client", "n2")
	var spans []HistorySpan
	for _, s := range n1 {
		// n1's own services are observed by n1's agent only while it runs.
		spans = append(spans, HistorySpan{Service: s, From: now.Add(-14 * day), To: now.Add(-8 * day)})
	}
	spans = append(spans, HistorySpan{Service: cl, From: now.Add(-14 * day), To: now})
	clientToAPI := &storage.Connection{ID: client + "->" + apiAsSeenByClient + ":8080", SourceID: client, TargetID: apiAsSeenByClient, Port: 8080}
	gwBill := &storage.Connection{ID: gw + "->" + billing + ":9000", SourceID: gw, TargetID: billing, Port: 9000}
	f.clientToAPI, f.gwBill = clientToAPI.ID, gwBill.ID
	spans = append(spans,
		// n2's agent keeps this edge (and, with resolved names, n1's api)
		// fresh throughout.
		HistorySpan{Connection: clientToAPI, From: now.Add(-14 * day), To: now},
		HistorySpan{Connection: gwBill, From: now.Add(-14 * day), To: now.Add(-8 * day)},
	)
	if apiAsSeenByClient == api {
		// Resolved names: the client's throughput refreshes api's row too.
		spans = append(spans, HistorySpan{Service: n1[0], From: now.Add(-8 * day), To: now})
	}
	if withHeartbeats {
		spans = append(spans,
			HistorySpan{Node: "n2", From: now.Add(-14 * day), To: now},
			HistorySpan{Node: "n1", From: now.Add(-14 * day), To: now.Add(-8 * day)},
			HistorySpan{Node: "n1", From: now.Add(-time.Hour), To: now},
		)
	}
	if err := ts.ImportHistory(ctx, spans); err != nil {
		t.Fatal(err)
	}
	// Current state: everything is there now (n1's agent is back).
	for _, s := range append(n1, cl) {
		cp := *s
		if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
			t.Fatal(err)
		}
	}
	if err := ts.AddConnection(ctx, &storage.Connection{ID: clientToAPI.ID, SourceID: client, TargetID: apiAsSeenByClient, Port: 8080, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
	return f
}

func (f *outageFixture) billingVerdict(t *testing.T) (item *IdleServiceItem, excluded *IdleExclusion) {
	t.Helper()
	r, err := f.ts.GetIdleSpend(context.Background(), IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Items {
		if r.Items[i].ID == f.billing {
			return &r.Items[i], nil
		}
	}
	for i := range r.Excluded {
		if r.Excluded[i].ID == f.billing {
			return nil, &r.Excluded[i]
		}
	}
	return nil, nil
}

// TestIdleNodeAgentOutageIsNotSilence pins the review's repro: billing must
// not be flagged (it was "orphan / medium / unobserved 0s" while a peer's
// observations were credited to n1).
func TestIdleNodeAgentOutageIsNotSilence(t *testing.T) {
	cases := []struct {
		name                          string
		api, billing, gw, client, see string
		heartbeats                    bool
	}{
		// v3.1: agents heartbeat; resolved (Kubernetes) names, where the
		// client's agent keeps api's own ID fresh.
		{"heartbeats, resolved names", "shop/api", "shop/billing", "shop/gw", "shop/client", "shop/api", true},
		// v3.1: heartbeats, address-keyed IDs.
		{"heartbeats, address IDs", "10.0.1.10/api", "10.0.1.11/billing", "10.0.1.12/gw", "10.0.2.5/client", "10.0.1.10:8080", true},
		// History from before heartbeats existed: only n1's own process-keyed
		// IDs can show n1 was watched.
		{"no heartbeats, address IDs", "10.0.1.10/api", "10.0.1.11/billing", "10.0.1.12/gw", "10.0.2.5/client", "10.0.1.10:8080", false},
		// Before heartbeats, with resolved names nothing proves n1's agent
		// was up: n1 counts as not observed rather than borrowing n2's
		// coverage.
		{"no heartbeats, resolved names", "shop/api", "shop/billing", "shop/gw", "shop/client", "shop/api", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOutageFixture(t, c.api, c.billing, c.gw, c.client, c.see, c.heartbeats)
			item, ex := f.billingVerdict(t)
			if item != nil {
				t.Fatalf("billing flagged as %s/%s with unobserved=%ds quiet=%ds: n1's agent was down for 8 of the last 8 days",
					item.Category, item.IdleConfidence, item.Evidence.UnobservedSeconds, item.Evidence.QuietSeconds)
			}
			if ex == nil || ex.Reason != IdleExcludedNotQuietEnough {
				t.Fatalf("billing should be held back as %s, got %+v", IdleExcludedNotQuietEnough, ex)
			}
		})
	}
}

// TestCoverageNoOwnReportsNoSilence is the invariant behind it: a service on
// a node whose own agent reported nothing during a window accrues no
// observed silence in that window, however much the node's peers reported
// about it - for every shape of ID a peer can use.
func TestCoverageNoOwnReportsNoSilence(t *testing.T) {
	now := time.Now()
	gapFrom, gapTo := now.Add(-8*day), now.Add(-time.Hour)
	nodeOf := map[string]string{
		"10.0.1.10/api": "n1", "shop/api": "n1", "10.0.1.10:8080": "n1", "10.0.1.10": "n1",
		"10.0.2.5/client": "n2", "shop/client": "n2", "10.0.2.5": "n2",
	}
	peerEdges := [][2]string{
		{"10.0.2.5/client", "10.0.1.10:8080"}, // n2's connect, as n2 records it
		{"shop/client", "shop/api"},           // resolved names, either side
		{"10.0.1.10/api", "10.0.2.5:443"},     // a call *from* n1 - only n1 can record it
		{"10.0.2.5", "10.0.1.10/api"},         // n1's accept, as n1 records it
	}
	for _, heartbeats := range []bool{false, true} {
		for _, pe := range peerEdges {
			t.Run(fmt.Sprintf("heartbeats=%v %s->%s", heartbeats, pe[0], pe[1]), func(t *testing.T) {
				in := idleInputs{now: now, maxGap: 5 * time.Minute, coverageStart: now.Add(-14 * day), historyEnabled: true}
				in.historyServices = []storage.ServiceInterval{
					{ServiceID: "10.0.1.10/api", Node: "n1", FirstSeen: now.Add(-14 * day), LastSeen: gapFrom},
					{ServiceID: "10.0.1.10/api", Node: "n1", FirstSeen: gapTo, LastSeen: now},
					{ServiceID: "10.0.2.5/client", Node: "n2", FirstSeen: now.Add(-14 * day), LastSeen: now},
				}
				// The peer's edge is observed throughout, the gap included.
				in.historyEdges = []storage.ConnectionInterval{
					{ConnectionID: "peer", SourceID: pe[0], TargetID: pe[1], FirstSeen: now.Add(-14 * day), LastSeen: now},
				}
				if heartbeats {
					in.historyNodes = []storage.NodeInterval{
						{Node: "n1", FirstSeen: now.Add(-14 * day), LastSeen: gapFrom},
						{Node: "n1", FirstSeen: gapTo, LastSeen: now},
						{Node: "n2", FirstSeen: now.Add(-14 * day), LastSeen: now},
					}
				}
				cov := buildIdleCoverage(in, func(id string) string { return nodeOf[id] })
				// Wholly inside the gap (clear of the grace after gapFrom).
				from, to := gapFrom.Add(time.Hour), gapTo.Add(-time.Minute)
				got := cov.observed([]string{"n1"}, from, to)
				// An edge with a process-keyed n1 endpoint can only have been
				// recorded by n1's agent: that is n1 reporting (with or
				// without heartbeats - a failed metrics post must not blind
				// the node).
				ownReports := localEndpointID(pe[0]) && nodeOf[pe[0]] == "n1" ||
					localEndpointID(pe[1]) && nodeOf[pe[1]] == "n1"
				if ownReports {
					// n1's own agent recorded the edge: that is evidence.
					if got == 0 {
						t.Errorf("an edge only n1's agent can record must count as n1 observing")
					}
					return
				}
				if got != 0 {
					t.Errorf("n1 observed %s of a window in which its own agent reported nothing", got)
				}
				if n2 := cov.observed([]string{"n2"}, from, to); n2 < to.Sub(from)-time.Minute {
					t.Errorf("n2 kept reporting but is credited only %s of %s", n2, to.Sub(from))
				}
			})
		}
	}
}

// TestCoverageHeartbeatsAndOwnActivity: a node is observed while its agent
// heartbeats OR its own local activity is recorded - a failing metrics post
// does not blind it - and an unknown node is not observed.
func TestCoverageHeartbeatsAndOwnActivity(t *testing.T) {
	now := time.Now()
	c := newObservationCoverage(time.Minute, time.Minute, 0)
	c.addEvidence("e1", []string{"n1"}, now.Add(-10*day), now.Add(-5*day))
	c.setHeartbeats([]storage.NodeInterval{{Node: "n1", FirstSeen: now.Add(-2 * day), LastSeen: now.Add(-day)}})
	if got := c.observed([]string{"n1"}, now.Add(-10*day), now.Add(-5*day)); got < 5*day {
		t.Errorf("own activity, no heartbeat: %s, want 5d", got)
	}
	if got := c.observed([]string{"n1"}, now.Add(-2*day), now.Add(-day)); got < day {
		t.Errorf("heartbeat: %s, want 1d", got)
	}
	if got := c.observed([]string{"n1"}, now.Add(-day+time.Hour), now); got != 0 {
		t.Errorf("neither: %s, want 0", got)
	}
	if got := c.observed([]string{"n9"}, now.Add(-10*day), now); got != 0 {
		t.Errorf("unknown node: %s, want 0", got)
	}
	if got := c.observed(nil, now.Add(-2*day), now.Add(-day)); got < day {
		t.Errorf("global: %s, want the heartbeat (1d)", got)
	}
}

// TestCoverageClusterGroup: a CLUSTER_NAME group is observed only while no
// current member is silent. A member is current from its first heartbeat
// until it has been silent for the member timeout (1h), so a member that
// stops for good holds the group up for an hour, not until it is retired.
func TestCoverageClusterGroup(t *testing.T) {
	now := time.Now()
	const g = "Kubernetes"
	c := newObservationCoverage(0, 0, 6*time.Hour)
	c.setHeartbeats([]storage.NodeInterval{
		{Node: "a", Group: g, FirstSeen: now.Add(-10 * day), LastSeen: now.Add(-8 * day)}, // dies, never returns
		{Node: "b", Group: g, FirstSeen: now.Add(-10 * day), LastSeen: now.Add(-day)},
		{Node: "b", Group: g, FirstSeen: now.Add(-day + 30*time.Minute), LastSeen: now},  // 30 min out
		{Node: "c", Group: g, FirstSeen: now.Add(-3 * day), LastSeen: now.Add(-2 * day)}, // joins, out for 2h, back
		{Node: "c", Group: g, FirstSeen: now.Add(-2*day + 2*time.Hour), LastSeen: now},
		{Node: "x", FirstSeen: now.Add(-10 * day), LastSeen: now},                 // another, ungrouped node
		{Node: "a", Group: "other", FirstSeen: now.Add(-10 * day), LastSeen: now}, // same name, other cluster
	})
	obs := func(from, to time.Time) time.Duration { return c.observed([]string{g}, from, to) }
	if got := obs(now.Add(-10*day), now.Add(-8*day)); got < 2*day-time.Minute {
		t.Errorf("all members up: %s, want 2d", got)
	}
	if got := obs(now.Add(-8*day), now.Add(-8*day+time.Hour)); got != 0 {
		t.Errorf("member a silent, within the member timeout: %s, want 0 - one live agent must not cover a dead one", got)
	}
	if got := obs(now.Add(-8*day+time.Hour), now.Add(-3*day)); got < 5*day-time.Hour-time.Minute {
		t.Errorf("a silent for longer than the timeout: %s, want b's coverage", got)
	}
	if got := obs(now.Add(-day), now.Add(-day+30*time.Minute)); got != 0 {
		t.Errorf("member b out for 30 minutes: %s, want 0", got)
	}
	if got := obs(now.Add(-2*day), now.Add(-2*day+time.Hour)); got != 0 {
		t.Errorf("member c out, first hour: %s, want 0", got)
	}
	if got := obs(now.Add(-2*day+time.Hour), now.Add(-2*day+2*time.Hour)); got < time.Hour-time.Minute {
		t.Errorf("member c out, second hour: %s, want 1h (no longer required)", got)
	}
	if got := obs(now.Add(-2*day+2*time.Hour), now.Add(-day)); got < day-2*time.Hour-time.Minute {
		t.Errorf("c back: %s, want covered", got)
	}
	// Agents are (group, node): the ungrouped node x and node a of another
	// cluster are unaffected by the group, and vice versa.
	if got := c.observed([]string{"x"}, now.Add(-8*day), now); got < 8*day {
		t.Errorf("ungrouped node x: %s", got)
	}
	if got := c.observed([]string{"other"}, now.Add(-8*day), now); got < 8*day {
		t.Errorf("group other (its own node a reporting): %s", got)
	}
	if got := c.observed([]string{"a"}, now.Add(-8*day), now); got != 0 {
		t.Errorf("ungrouped node a (never reported ungrouped): %s, want 0", got)
	}
	silent := c.silentAgents(now.Add(-9*day), now, true)
	if len(silent) != 1 || silent[0].Node != "a" || silent[0].Group != g {
		t.Errorf("silent agents = %+v, want a in %s", silent, g)
	}
}

// TestCoverageGroupWithoutHeartbeats: a known group with no member
// heartbeats is unobserved - local evidence recorded under the group name is
// not used.
func TestCoverageGroupWithoutHeartbeats(t *testing.T) {
	now := time.Now()
	c := newObservationCoverage(time.Minute, time.Minute, 0)
	c.addEvidence("", []string{"Kubernetes"}, now.Add(-10*day), now)
	c.setGroups([]string{"Kubernetes"})
	if got := c.observed([]string{"Kubernetes"}, now.Add(-10*day), now); got != 0 {
		t.Errorf("group before any member heartbeat: %s, want 0", got)
	}
	if got := c.total(now.Add(-10*day), now); got < 10*day-time.Minute {
		t.Errorf("global: %s, want the evidence", got)
	}
}

// TestCoverageFlappingMember: a crash-looping member (5 minutes of
// heartbeats every 20) stops holding the group up once it has reported for
// less than half of the preceding two hours; until then its gaps count as
// unobserved.
func TestCoverageFlappingMember(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	const g = "prod"
	start := now.Add(-12 * time.Hour)
	beats := []storage.NodeInterval{
		{Node: "steady", Group: g, FirstSeen: now.Add(-day), LastSeen: now},
		{Node: "flappy", Group: g, FirstSeen: now.Add(-day), LastSeen: start},
	}
	for t := start.Add(20 * time.Minute); t.Before(now); t = t.Add(20 * time.Minute) {
		beats = append(beats, storage.NodeInterval{Node: "flappy", Group: g, FirstSeen: t, LastSeen: t.Add(5 * time.Minute)})
	}
	c := newObservationCoverage(0, 0, 72*time.Hour)
	c.setHeartbeats(beats)
	obs := func(from, to time.Time) time.Duration { return c.observed([]string{g}, from, to) }
	// First hour of flapping: still current, mostly unobserved.
	if got := obs(start, start.Add(time.Hour)); got > 20*time.Minute {
		t.Errorf("first hour of flapping: %s observed, want at most its reporting time", got)
	}
	// Ratio over the last 2h falls under 50% after 80 minutes: from then on
	// the group is observed.
	if got := obs(start.Add(2*time.Hour), now); got < 10*time.Hour-time.Minute {
		t.Errorf("after the flapping rule applies: %s observed of 10h", got)
	}
}

// TestCoverageRetirement: an outage shorter than retireAfter accrues
// nothing; a node silent for longer, while others keep reporting, follows
// the global coverage from then on.
func TestCoverageRetirement(t *testing.T) {
	now := time.Now()
	c := newObservationCoverage(0, 0, 6*time.Hour)
	c.setHeartbeats([]storage.NodeInterval{
		{Node: "a", FirstSeen: now.Add(-2 * day), LastSeen: now.Add(-day)},
		{Node: "b", FirstSeen: now.Add(-2 * day), LastSeen: now},
		{Node: "short", FirstSeen: now.Add(-2 * day), LastSeen: now.Add(-4 * time.Hour)},
	})
	if got := c.observed([]string{"a"}, now.Add(-day), now.Add(-day+6*time.Hour)); got != 0 {
		t.Errorf("a within the retirement threshold: %s, want 0", got)
	}
	if got := c.observed([]string{"a"}, now.Add(-day+6*time.Hour), now); got < 18*time.Hour-time.Minute {
		t.Errorf("a retired: %s, want the global 18h", got)
	}
	if got := c.observed([]string{"short"}, now.Add(-4*time.Hour), now); got != 0 {
		t.Errorf("4h outage: %s, want 0", got)
	}
}

func TestCoverageMinus(t *testing.T) {
	at := func(h int) time.Time { return time.Unix(0, 0).Add(time.Duration(h) * time.Hour) }
	sp := func(a, b int) span { return span{at(a), at(b)} }
	got := coverage{sp(0, 10), sp(20, 30)}.minus(coverage{sp(2, 3), sp(5, 22), sp(25, 26), sp(29, 40)})
	want := coverage{sp(0, 2), sp(3, 5), sp(22, 25), sp(26, 29)}
	if len(got) != len(want) {
		t.Fatalf("minus = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].from.Equal(want[i].from) || !got[i].to.Equal(want[i].to) {
			t.Fatalf("minus = %v, want %v", got, want)
		}
	}
}

// TestEdgeReporters: an edge's absence is measured against the nodes of its
// process-keyed endpoints; failing those, the node on the source's service
// row, else the target's; only node-less edges use the global coverage.
// Evidence of observation is credited to process-keyed endpoints only.
func TestEdgeReporters(t *testing.T) {
	nodes := map[string]string{
		"10.0.1.10/api": "node-a", "Deploy:prod/api": "node-a", "Deploy:prod/web": "Kubernetes",
		"Svc:prod/db": "node-b", "10.0.3.10:5432": "",
	}
	nodeOf := func(id string) string { return nodes[id] }
	for _, c := range []struct {
		src, tgt string
		want     string
		evidence string
	}{
		{"10.0.1.10/api", "10.0.3.10:5432", "node-a", "node-a"},
		{"Deploy:prod/api", "Svc:prod/db", "node-a", ""},
		{"Deploy:prod/web", "Svc:prod/db", "Kubernetes", ""},
		{"Deploy:prod/unknown", "Svc:prod/db", "node-b", ""},
		{"203.0.113.9", "198.51.100.7:443", "", ""},
	} {
		if got := strings.Join(edgeReporters(c.src, c.tgt, nodeOf), ","); got != c.want {
			t.Errorf("edgeReporters(%s, %s) = %q, want %q", c.src, c.tgt, got, c.want)
		}
		if got := strings.Join(edgeEvidenceNodes(c.src, c.tgt, nodeOf), ","); got != c.evidence {
			t.Errorf("edgeEvidenceNodes(%s, %s) = %q, want %q", c.src, c.tgt, got, c.evidence)
		}
	}
}
