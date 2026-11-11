package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Regression tests from the second review of the coverage fix (ported from
// its probes): CLUSTER_NAME groups, retired nodes, failing metrics posts and
// the v3.0 upgrade.

// driftScenario drives a detector over a two-node world: node a runs api
// (api->db, api->cache), node b runs web (web->redis). When group is set,
// both agents report under it (CLUSTER_NAME): services carry the group as
// their node and heartbeats are recorded per real node with the group.
type driftScenario struct {
	h     *driftHarness
	d     *DriftDetector
	group string
}

const (
	scAPI   = "10.0.1.10/api"
	scDB    = "10.0.3.10:5432"
	scCache = "10.0.3.11:6379"
	scWeb   = "10.0.2.10/web"
	scRedis = "10.0.2.20:6379"
)

func newDriftScenario(t *testing.T, group string) *driftScenario {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ctx := context.Background()
	na, nb := "node-a", "node-b"
	if group != "" {
		na, nb = group, group
	}
	for _, s := range []storage.Service{{ID: scAPI, Name: "api", Node: na}, {ID: scWeb, Name: "web", Node: nb}} {
		s := s
		if err := h.store.Services().Upsert(ctx, &s); err != nil {
			t.Fatal(err)
		}
	}
	return &driftScenario{h: h, group: group}
}

func (s *driftScenario) beat(node string) {
	if err := s.h.store.History().RecordNode(context.Background(), node, s.group, s.h.clock, 5*time.Minute); err != nil {
		s.h.t.Fatal(err)
	}
}

// run advances `d` of time in 2-minute steps, calling each before the step
// and checking every 10 minutes once the detector exists.
func (s *driftScenario) run(d time.Duration, each func()) {
	for el := time.Duration(0); el < d; el += 2 * time.Minute {
		each()
		s.h.advance(2 * time.Minute)
		if s.d != nil && el%(10*time.Minute) == 0 {
			s.d.step(context.Background())
		}
	}
}

func (s *driftScenario) start() {
	s.d = s.h.detectorWith(DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}, nil)
}

func (s *driftScenario) nodeA(api2db bool, heartbeat bool) {
	if api2db {
		s.h.see(scAPI, scDB, 5432)
	}
	s.h.see(scAPI, scCache, 6379)
	if heartbeat {
		s.beat("node-a")
	}
}

func (s *driftScenario) nodeB() {
	s.h.see(scWeb, scRedis, 6379)
	s.beat("node-b")
}

func gone(feed []storage.DriftEvent) []string {
	var out []string
	for _, ev := range feed {
		if ev.Kind == storage.DriftDisappeared {
			out = append(out, ev.ConnectionID)
		}
	}
	return out
}

func wantGone(t *testing.T, feed []storage.DriftEvent, want ...string) {
	t.Helper()
	got := gone(feed)
	if strings.Join(sortedCopy(got), ",") != strings.Join(sortedCopy(want), ",") {
		t.Errorf("gone = %v, want %v (feed %v)", got, want, feedIDs(feed))
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// A node that is decommissioned (its agent never comes back) while the rest
// keeps reporting: after the retirement threshold (6h floor at a 30m
// GoneAfter) its edges are reported gone - once - instead of never. v3.0
// reported them; the first coverage fix had gone silent for good.
func TestDriftRetiredNodeIsReportedGone(t *testing.T) {
	for _, group := range []string{"", "prod"} {
		name := "per-node"
		if group != "" {
			name = "CLUSTER_NAME group"
		}
		t.Run(name, func(t *testing.T) {
			s := newDriftScenario(t, group)
			s.run(2*time.Hour, func() { s.nodeA(true, true); s.nodeB() })
			s.start()

			s.run(6*time.Hour, s.nodeB) // inside the retirement threshold
			if g := gone(s.h.feed()); len(g) != 0 {
				t.Fatalf("reported gone before retirement: %v", g)
			}
			s.run(4*time.Hour, s.nodeB)
			apiDB, apiCache := scAPI+"->"+scDB, scAPI+"->"+scCache
			wantGone(t, s.h.feed(), apiDB, apiCache)
			s.run(2*time.Hour, s.nodeB)
			wantGone(t, s.h.feed(), apiDB, apiCache) // once
		})
	}
}

// A single agent outage shorter than the retirement threshold produces
// nothing, also for a CLUSTER_NAME group (where one live member used to keep
// the dead one "observed"), and nothing on recovery.
func TestDriftGroupMemberOutageIsQuiet(t *testing.T) {
	s := newDriftScenario(t, "prod")
	s.run(2*time.Hour, func() { s.nodeA(true, true); s.nodeB() })
	s.start()
	s.run(3*time.Hour, s.nodeB) // node-a's agent down 6x GoneAfter
	s.run(time.Hour, func() { s.nodeA(true, true); s.nodeB() })
	if ev := s.h.feed(); len(ev) != 0 {
		t.Fatalf("group member outage reported as drift: %v", feedIDs(ev))
	}
}

// node-a's agent keeps sending events and throughput but its metrics posts
// fail (no heartbeats ever): its own traffic keeps it observed, so a real
// disappearance (api->db) is reported within about GoneAfter.
func TestDriftMetricsFailureDoesNotBlindNode(t *testing.T) {
	s := newDriftScenario(t, "")
	s.run(2*time.Hour, func() { s.nodeA(true, false); s.nodeB() })
	s.start()
	s.run(90*time.Minute, func() { s.nodeA(false, false); s.nodeB() })
	wantGone(t, s.h.feed(), scAPI+"->"+scDB)
}

// The v3.0 upgrade: history without heartbeats, api->db stops 10 minutes
// before the upgrade, heartbeats start afterwards. The disappearance is
// reported after about GoneAfter of observation; nothing else is.
func TestDriftAcrossTheHeartbeatUpgrade(t *testing.T) {
	for _, ids := range [][3]string{
		{"10.0.1.10/api", "10.0.3.10:5432", "10.0.3.11:6379"},
		{"Deploy:prod/api", "Svc:prod/db", "Svc:prod/cache"},
	} {
		t.Run(ids[0], func(t *testing.T) {
			h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
			ctx := context.Background()
			api, db, cache := ids[0], ids[1], ids[2]
			for _, s := range []storage.Service{{ID: api, Name: "api", Node: "node-a"}, {ID: scWeb, Name: "web", Node: "node-b"}} {
				s := s
				if err := h.store.Services().Upsert(ctx, &s); err != nil {
					t.Fatal(err)
				}
			}
			beat := func(node string) {
				if err := h.store.History().RecordNode(ctx, node, "", h.clock, 5*time.Minute); err != nil {
					t.Fatal(err)
				}
			}
			for el := time.Duration(0); el < 48*time.Hour; el += 2 * time.Minute {
				if el < 48*time.Hour-10*time.Minute {
					h.see(api, db, 5432)
				}
				h.see(api, cache, 6379)
				h.see(scWeb, scRedis, 6379)
				h.advance(2 * time.Minute)
			}
			d := h.detectorWith(DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}, nil)
			for el := time.Duration(0); el < 3*time.Hour; el += 2 * time.Minute {
				h.see(api, cache, 6379)
				h.see(scWeb, scRedis, 6379)
				beat("node-a")
				beat("node-b")
				h.advance(2 * time.Minute)
				d.step(ctx)
			}
			wantGone(t, h.feed(), api+"->"+db)
		})
	}
}

// The review's Idle-spend probe under the Helm default (agent.clusterName
// "Kubernetes"): node-a's agent dead for 8 days, node-b heartbeating under
// the same group. billing on node-a is not flagged - it was "orphan / medium
// / unobserved 0s, $200/mo" while the group heartbeat counted for both.
// Also: members joining and leaving.
func TestIdleClusterGroupCoverage(t *testing.T) {
	const g = "Kubernetes"
	type member struct {
		node     string
		from, to time.Duration // ago
	}
	cases := []struct {
		name     string
		members  []member
		wantFlag bool
	}{
		{"one member dead for 8 days", []member{{"node-a", 14 * day, 8 * day}, {"node-b", 14 * day, 0}}, false},
		{"all members up throughout", []member{{"node-a", 14 * day, 0}, {"node-b", 14 * day, 0}}, true},
		{"a member joined 2 days ago", []member{{"node-a", 14 * day, 0}, {"node-b", 14 * day, 0}, {"node-c", 2 * day, 0}}, true},
		// Scaled in 13 days ago: retired after the stale threshold (7d), so
		// the group is observed again for the last 6 days - enough to flag.
		{"a member removed 13 days ago", []member{{"node-a", 14 * day, 0}, {"node-b", 14 * day, 0}, {"node-old", 14 * day, 13 * day}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, _ := newHistoryTestService(t, true)
			ctx := context.Background()
			now := time.Now()
			billing := &storage.Service{ID: "shop/billing", Name: "billing", Type: "web_server", Node: g}
			gw := &storage.Service{ID: "shop/gw", Name: "gw", Type: "web_server", Node: g}
			cl := &storage.Service{ID: "shop/client", Name: "client", Type: "web_server", Node: g}
			gwBill := &storage.Connection{ID: "gw->billing", SourceID: gw.ID, TargetID: billing.ID, Port: 9000}
			spans := []HistorySpan{
				{Service: billing, From: now.Add(-14 * day), To: now},
				{Service: gw, From: now.Add(-14 * day), To: now},
				{Service: cl, From: now.Add(-14 * day), To: now},
				{Connection: gwBill, From: now.Add(-14 * day), To: now.Add(-8 * day)},
			}
			for _, m := range c.members {
				spans = append(spans, HistorySpan{Node: m.node, Group: g, From: now.Add(-m.from), To: now.Add(-m.to)})
			}
			if err := ts.ImportHistory(ctx, spans); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*storage.Service{billing, gw, cl} {
				cp := *s
				if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
					t.Fatal(err)
				}
			}
			ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
			r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
			if err != nil {
				t.Fatal(err)
			}
			var item *IdleServiceItem
			for i := range r.Items {
				if r.Items[i].ID == billing.ID {
					item = &r.Items[i]
				}
			}
			if c.wantFlag && item == nil {
				t.Fatalf("billing should be flagged: items=%+v excluded=%+v", r.Items, r.Excluded)
			}
			if !c.wantFlag && item != nil {
				t.Fatalf("billing flagged %s/%s unobserved=%ds: a group member's agent was down",
					item.Category, item.IdleConfidence, item.Evidence.UnobservedSeconds)
			}
			if !c.wantFlag {
				for _, ex := range r.Excluded {
					if ex.ID == billing.ID && ex.Reason != IdleExcludedNotQuietEnough {
						t.Errorf("billing held back as %s, want %s", ex.Reason, IdleExcludedNotQuietEnough)
					}
				}
			}
		})
	}
}

// Grouped agents' heartbeats are recorded per real node, with the group: the
// metrics path (processor) must not record a group-level heartbeat.
func TestGroupedAgentHeartbeatIsPerNode(t *testing.T) {
	ts, store := newHistoryTestService(t, true)
	p := NewEventProcessor(ts, nil)
	ctx := context.Background()
	for _, node := range []string{"node-a", "node-b"} {
		if err := p.ProcessHostMetrics(ctx, HostMetrics{NodeName: node, ClusterName: "Kubernetes", CPUPercent: 1, MemPercent: 1}); err != nil {
			t.Fatal(err)
		}
	}
	beats, err := store.History().NodesBetween(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range beats {
		got[b.Node] = b.Group
	}
	if len(got) != 2 || got["node-a"] != "Kubernetes" || got["node-b"] != "Kubernetes" {
		t.Errorf("heartbeats = %v, want node-a and node-b in group Kubernetes", got)
	}
	if m, _ := store.Metrics().Get(ctx, "Kubernetes"); m == nil {
		t.Error("the group's metrics row (what the UI shows) is gone")
	}
}
