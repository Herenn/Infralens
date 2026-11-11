package service

import (
	"context"
	"fmt"
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
				ownReports := !heartbeats && localEndpointID(pe[0]) && nodeOf[pe[0]] == "n1" ||
					!heartbeats && localEndpointID(pe[1]) && nodeOf[pe[1]] == "n1"
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

// TestCoverageEpoch: before the first heartbeat the edge evidence decides;
// from it on only heartbeats do.
func TestCoverageEpoch(t *testing.T) {
	now := time.Now()
	c := newObservationCoverage(time.Minute, time.Minute)
	c.addEvidence("e1", []string{"n1"}, now.Add(-10*day), now)
	c.addHeartbeat("n1", now.Add(-2*day), now.Add(-day))
	if got := c.observed([]string{"n1"}, now.Add(-10*day), now.Add(-2*day)); got < 8*day-time.Minute {
		t.Errorf("pre-epoch: %s, want the edge evidence (8d)", got)
	}
	if got := c.observed([]string{"n1"}, now.Add(-day+time.Hour), now); got != 0 {
		t.Errorf("post-epoch without heartbeats: %s, want 0 despite edge evidence", got)
	}
	if got := c.observed([]string{"n9"}, now.Add(-day+time.Hour), now); got != 0 {
		t.Errorf("unknown node after the epoch: %s, want 0", got)
	}
	if got := c.observed(nil, now.Add(-2*day), now.Add(-day)); got < day {
		t.Errorf("global after the epoch: %s, want the heartbeat (1d)", got)
	}
}
