package service

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Regression tests ported from the third review's probes: autoscaled
// CLUSTER_NAME groups, Kubernetes-named edges, flapping agents, dead agents
// in Idle spend, and evidence memory.

// probeWorld: node-a runs api (api->db, api->cache), node-b runs web
// (web->redis), node-c runs worker (worker->q). group != "" means
// CLUSTER_NAME: services carry the group as their node.
type probeWorld struct {
	h     *driftHarness
	group string
	ids   map[string]string
}

func newProbeWorld(t *testing.T, group string, k8s bool) *probeWorld {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	ids := map[string]string{
		"api": "10.0.1.10/api", "db": "10.0.3.10:5432", "cache": "10.0.3.11:6379",
		"web": "10.0.2.10/web", "redis": "10.0.2.20:6379",
		"worker": "10.0.4.10/worker", "q": "10.0.3.12:5672",
	}
	if k8s {
		ids = map[string]string{
			"api": "Deploy:prod/api", "db": "Svc:prod/db", "cache": "Svc:prod/cache",
			"web": "Deploy:prod/web", "redis": "Svc:prod/redis",
			"worker": "Deploy:prod/worker", "q": "Svc:prod/q",
		}
	}
	node := func(n string) string {
		if group != "" {
			return group
		}
		return n
	}
	ctx := context.Background()
	for _, s := range []storage.Service{
		{ID: ids["api"], Name: "api", Node: node("node-a")},
		{ID: ids["web"], Name: "web", Node: node("node-b")},
		{ID: ids["worker"], Name: "worker", Node: node("node-c")},
	} {
		s := s
		if err := h.store.Services().Upsert(ctx, &s); err != nil {
			t.Fatal(err)
		}
	}
	return &probeWorld{h: h, group: group, ids: ids}
}

func (w *probeWorld) beat(n string) {
	if err := w.h.store.History().RecordNode(context.Background(), n, w.group, w.h.clock, 5*time.Minute); err != nil {
		w.h.t.Fatal(err)
	}
}

func (w *probeWorld) see(a, b string, port uint16) { w.h.see(w.ids[a], w.ids[b], port) }

func (w *probeWorld) edge(a, b string) string { return w.ids[a] + "->" + w.ids[b] }

// run advances in 2-minute steps, stepping the detector every 10 minutes,
// and returns when each edge was first reported gone (elapsed time).
func (w *probeWorld) run(d *DriftDetector, dur time.Duration, each func(el time.Duration)) map[string]time.Duration {
	goneAt := map[string]time.Duration{}
	for el := time.Duration(0); el < dur; el += 2 * time.Minute {
		each(el)
		w.h.advance(2 * time.Minute)
		if el%(10*time.Minute) == 0 {
			d.step(context.Background())
			for _, e := range w.h.feed() {
				if _, ok := goneAt[e.ConnectionID]; !ok && e.Kind == storage.DriftDisappeared {
					goneAt[e.ConnectionID] = el
				}
			}
		}
	}
	return goneAt
}

func (w *probeWorld) warm(d time.Duration, nodes ...string) {
	for el := time.Duration(0); el < d; el += 2 * time.Minute {
		w.see("api", "db", 5432)
		w.see("api", "cache", 6379)
		w.see("web", "redis", 6379)
		w.see("worker", "q", 5672)
		for _, n := range nodes {
			w.beat(n)
		}
		w.h.advance(2 * time.Minute)
	}
}

func goneKeys(m map[string]time.Duration) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return strings.Join(sortedCopy(ks), ",")
}

// Helm default (one CLUSTER_NAME group): 5 steady nodes, and the autoscaler
// adds a node every `every` and removes it 12h later. Each scale-in costs
// the group the one-hour member timeout, no more. (v3.1.0-rc: 0-2% observed
// with daily scale-in.)
func TestReviewProbeAutoscaleGroup(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		every time.Duration
		min   float64 // observed share, last 7d and 30d
	}{{24 * time.Hour, 95}, {3 * 24 * time.Hour, 98}, {10 * 24 * time.Hour, 99}} {
		var beats []storage.NodeInterval
		for i := 0; i < 5; i++ {
			beats = append(beats, storage.NodeInterval{Node: fmt.Sprintf("steady-%d", i), Group: "Kubernetes", FirstSeen: now.Add(-30 * day), LastSeen: now})
		}
		k := 0
		for at := now.Add(-30 * day); at.Before(now); at = at.Add(c.every) {
			end := at.Add(12 * time.Hour)
			if end.After(now) {
				end = now
			}
			beats = append(beats, storage.NodeInterval{Node: fmt.Sprintf("burst-%d", k), Group: "Kubernetes", FirstSeen: at, LastSeen: end})
			k++
		}
		for _, ra := range []struct {
			name string
			d    time.Duration
		}{{"idle(7d)", 7 * day}, {"drift(72h)", 72 * time.Hour}} {
			cov := newObservationCoverage(5*time.Minute, 5*time.Minute, ra.d)
			cov.setHeartbeats(beats)
			w7 := 100 * cov.observed([]string{"Kubernetes"}, now.Add(-7*day), now).Hours() / (7 * 24)
			w30 := 100 * cov.observed([]string{"Kubernetes"}, now.Add(-30*day), now).Hours() / (30 * 24)
			t.Logf("scale-in every %-6s %-10s group observed: last 7d %5.1f%%, last 30d %5.1f%%", c.every, ra.name, w7, w30)
			if w7 < c.min || w30 < c.min {
				t.Errorf("scale-in every %s (%s): observed %.1f%% / %.1f%%, want >= %.0f%%", c.every, ra.name, w7, w30, c.min)
			}
		}
	}
}

// End to end: a genuinely idle service (no traffic for 20 days) in the
// Helm-default group is found, with and without daily scale-in churn. With
// churn, scaled-in members went silent within the window: low confidence,
// out of the total, a note naming them, and the evidence window counts the
// member timeouts as unobserved.
func TestReviewProbeAutoscaleIdleE2E(t *testing.T) {
	for _, churn := range []bool{false, true} {
		t.Run(fmt.Sprint("churn=", churn), func(t *testing.T) {
			ts, _ := newHistoryTestService(t, true)
			ctx := t.Context()
			now := time.Now()
			const g = "Kubernetes"
			legacy := &storage.Service{ID: "Deploy:prod/legacy", Name: "legacy", Type: "web_server", Node: g}
			web := &storage.Service{ID: "Deploy:prod/web", Name: "web", Type: "web_server", Node: g}
			ing := &storage.Service{ID: "Deploy:prod/ingress", Name: "ingress", Type: "web_server", Node: g}
			old := &storage.Connection{ID: "ingress->legacy", SourceID: ing.ID, TargetID: legacy.ID, Port: 8080}
			live := &storage.Connection{ID: "ingress->web", SourceID: ing.ID, TargetID: web.ID, Port: 8080}
			spans := []HistorySpan{
				{Service: legacy, From: now.Add(-29 * day), To: now},
				{Service: web, From: now.Add(-29 * day), To: now},
				{Service: ing, From: now.Add(-29 * day), To: now},
				{Connection: old, From: now.Add(-29 * day), To: now.Add(-20 * day)},
				{Connection: live, From: now.Add(-29 * day), To: now},
			}
			for i := 0; i < 3; i++ {
				spans = append(spans, HistorySpan{Node: fmt.Sprintf("steady-%d", i), Group: g, From: now.Add(-29 * day), To: now})
			}
			bursts := 0
			if churn {
				for at := now.Add(-29 * day); at.Before(now.Add(-12 * time.Hour)); at = at.Add(24 * time.Hour) {
					spans = append(spans, HistorySpan{Node: fmt.Sprintf("burst-%d", bursts), Group: g, From: at, To: at.Add(12 * time.Hour)})
					bursts++
				}
			}
			if err := ts.ImportHistory(ctx, spans); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*storage.Service{legacy, web, ing} {
				cp := *s
				if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
					t.Fatal(err)
				}
			}
			if err := ts.AddConnection(ctx, &storage.Connection{ID: live.ID, SourceID: ing.ID, TargetID: web.ID, Port: 8080, LastSeen: now}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				ts.NoteAgentNode(g, fmt.Sprintf("steady-%d", i))
			}
			ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
			r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
			if err != nil {
				t.Fatal(err)
			}
			var item *IdleServiceItem
			for i := range r.Items {
				it := &r.Items[i]
				t.Logf("FLAGGED %s cat=%s conf=%s in_total=%v unobserved=%ds quiet=%ds", it.ID, it.Category, it.IdleConfidence, it.InTotal, it.Evidence.UnobservedSeconds, it.Evidence.QuietSeconds)
				if it.ID == legacy.ID {
					item = it
				}
			}
			t.Logf("evidence observed=%ds unobserved=%ds silent=%d notes=%q", r.Evidence.ObservedSeconds, r.Evidence.UnobservedSeconds, len(r.SilentAgents), r.Notes)
			if item == nil || item.Category != IdleCategoryOrphan {
				t.Fatalf("the idle service is not flagged as orphan: %+v excluded=%+v", item, r.Excluded)
			}
			if !churn {
				if item.IdleConfidence != ConfidenceMedium || !item.InTotal || r.Evidence.UnobservedSeconds != 0 || len(r.SilentAgents) != 0 {
					t.Errorf("no churn: conf=%s in_total=%v unobserved=%ds silent=%+v", item.IdleConfidence, item.InTotal, r.Evidence.UnobservedSeconds, r.SilentAgents)
				}
				return
			}
			if item.IdleConfidence != ConfidenceLow || item.InTotal {
				t.Errorf("churn: conf=%s in_total=%v, want low / not in total", item.IdleConfidence, item.InTotal)
			}
			if !strings.Contains(r.Notes[0], "stopped reporting") || !strings.Contains(r.Notes[0], fmt.Sprintf("burst-%d", bursts-1)) {
				t.Errorf("first note does not name the silent members: %q", r.Notes[0])
			}
			// One member timeout per scale-in, not a whole group outage.
			if u := time.Duration(r.Evidence.UnobservedSeconds) * time.Second; u < time.Duration(bursts-2)*time.Hour || u > time.Duration(bursts+1)*time.Hour {
				t.Errorf("unobserved %s for %d scale-ins, want about an hour each", u, bursts)
			}
		})
	}
}

// Evidence memory scales with nodes and extendable edges, not intervals.
// (v3.1.0-rc: 431 MB for a million intervals.)
func TestReviewEvidenceMemory(t *testing.T) {
	now := time.Now()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	c := newObservationCoverage(5*time.Minute, time.Hour, 72*time.Hour)
	const n = 1000000
	for i := 0; i < n; i++ {
		from := now.Add(-time.Duration(i%720) * time.Hour)
		c.addEvidence(fmt.Sprintf("%d|10.0.%d.%d/proc->10.9.%d.%d:5432:5432", i, i>>8&255, i&255, i>>12&255, i>>4&255), []string{fmt.Sprintf("node-%d", i%500)}, from, from.Add(time.Hour))
	}
	c.globalCoverage()
	for i := 0; i < 500; i++ {
		c.observed([]string{fmt.Sprintf("node-%d", i)}, now.Add(-24*time.Hour), now)
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	mb := (float64(m1.HeapAlloc) - float64(m0.HeapAlloc)) / 1e6
	t.Logf("retained for %d evidence intervals: %.0f MB", n, mb)
	if mb > 64 {
		t.Errorf("retained %.0f MB for %d evidence intervals, budget 64 MB", mb, n)
	}
	runtime.KeepAlive(c)
}

// A silent agent: decommissioned (no heartbeats, no traffic), failing
// metrics posts (traffic, no heartbeats), and a silent CLUSTER_NAME member.
func TestReviewProbeDriftSilent(t *testing.T) {
	cfg := DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}
	for _, mode := range []string{"decommissioned", "metrics-failing", "cluster-name"} {
		t.Run(mode, func(t *testing.T) {
			g := ""
			if mode == "cluster-name" {
				g = "prod"
			}
			w := newProbeWorld(t, g, false)
			w.warm(2*time.Hour, "node-a", "node-b", "node-c")
			d := w.h.detectorWith(cfg, nil)
			goneAt := w.run(d, 10*time.Hour, func(el time.Duration) {
				if mode == "metrics-failing" {
					w.see("api", "cache", 6379) // api->db really went away; no heartbeats from node-a
				}
				w.see("web", "redis", 6379)
				w.see("worker", "q", 5672)
				w.beat("node-b")
				w.beat("node-c")
			})
			t.Logf("%s: gone at %v", mode, goneAt)
			apiDB, apiCache := w.edge("api", "db"), w.edge("api", "cache")
			switch mode {
			case "decommissioned": // retired after the 6h floor
				if goneKeys(goneAt) != strings.Join(sortedCopy([]string{apiDB, apiCache}), ",") || goneAt[apiDB] < 6*time.Hour {
					t.Errorf("gone = %v, want both api edges after 6h", goneAt)
				}
			case "metrics-failing":
				if goneKeys(goneAt) != apiDB || goneAt[apiDB] > time.Hour {
					t.Errorf("gone = %v, want only api->db within an hour", goneAt)
				}
			case "cluster-name": // member timeout + GoneAfter
				if goneKeys(goneAt) != strings.Join(sortedCopy([]string{apiDB, apiCache}), ",") || goneAt[apiDB] < 80*time.Minute || goneAt[apiDB] > 2*time.Hour {
					t.Errorf("gone = %v, want both api edges after about 1h30m", goneAt)
				}
			}
		})
	}
}

// Kubernetes-named edges are measured against the node on the source's
// service row: node-a's agent down for 3h (under the 6h retirement floor)
// produces no "gone" on a per-node install, while a real disappearance on
// node-b during the outage is still reported. In a CLUSTER_NAME group the
// silent member stops holding the group after the member timeout, so its
// edges are reported after timeout + GoneAfter (1h30m here; with the default
// 24h GoneAfter, a day later).
func TestReviewProbeDriftK8sNames(t *testing.T) {
	for _, c := range []struct {
		group     string
		goneAfter time.Duration
		realGone  bool
	}{
		{"", 30 * time.Minute, false},
		{"", 30 * time.Minute, true},
		{"Kubernetes", 30 * time.Minute, false},
		{"Kubernetes", 30 * time.Minute, true},
		{"Kubernetes", DefaultDriftGoneAfter, false},
	} {
		t.Run(fmt.Sprintf("group=%q/goneAfter=%s/realGone=%v", c.group, c.goneAfter, c.realGone), func(t *testing.T) {
			w := newProbeWorld(t, c.group, true)
			w.warm(2*time.Hour, "node-a", "node-b", "node-c")
			d := w.h.detectorWith(DriftConfig{GoneAfter: c.goneAfter, Warmup: 0}, nil)
			goneAt := w.run(d, 3*time.Hour, func(el time.Duration) {
				if !c.realGone {
					w.see("web", "redis", 6379)
				}
				w.see("worker", "q", 5672)
				w.beat("node-b")
				w.beat("node-c")
			})
			t.Logf("node-a agent down 3h: gone at %v", goneAt)
			webRedis, apiDB, apiCache := w.edge("web", "redis"), w.edge("api", "db"), w.edge("api", "cache")
			var want []string
			if c.realGone {
				want = append(want, webRedis)
			}
			if c.group != "" && c.goneAfter < time.Hour {
				want = append(want, apiDB, apiCache)
				if goneAt[apiDB] < 80*time.Minute {
					t.Errorf("api->db gone after %s, before the member timeout + GoneAfter", goneAt[apiDB])
				}
			}
			if goneKeys(goneAt) != strings.Join(sortedCopy(want), ",") {
				t.Errorf("gone = %v, want %v", goneAt, want)
			}
			// In a group the silent member holds everything up for its first
			// hour, so a real disappearance waits for the member timeout too.
			limit := time.Hour
			if c.group != "" {
				limit = 2 * time.Hour
			}
			if c.realGone && goneAt[webRedis] > limit {
				t.Errorf("real disappearance reported after %s, want within %s", goneAt[webRedis], limit)
			}
		})
	}
}

// A flapping member (node-c heartbeats once every 20 minutes) does not hold
// a group unobserved indefinitely: a real disappearance on node-b is
// reported within about two hours. A single-node install whose only agent
// goes silent reports nothing - nothing at all is observed.
func TestReviewProbeDriftFlappingAndSingleNode(t *testing.T) {
	cfg := DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}
	for _, g := range []string{"", "prod"} {
		t.Run("flap group="+g, func(t *testing.T) {
			w := newProbeWorld(t, g, false)
			w.warm(2*time.Hour, "node-a", "node-b", "node-c")
			d := w.h.detectorWith(cfg, nil)
			goneAt := w.run(d, 12*time.Hour, func(el time.Duration) {
				w.see("api", "db", 5432)
				w.see("api", "cache", 6379)
				// web->redis gone for real at t=0
				w.see("worker", "q", 5672)
				w.beat("node-a")
				w.beat("node-b")
				if int(el/time.Minute)%20 < 2 {
					w.beat("node-c")
				}
			})
			t.Logf("flapping node-c, group=%q: gone at %v", g, goneAt)
			webRedis := w.edge("web", "redis")
			limit := time.Hour
			if g != "" {
				limit = 2 * time.Hour
			}
			if goneKeys(goneAt) != webRedis || goneAt[webRedis] > limit {
				t.Errorf("gone = %v, want only web->redis within %s", goneAt, limit)
			}
		})
	}
	t.Run("single node, agent removed", func(t *testing.T) {
		w := newProbeWorld(t, "", false)
		w.warm(2*time.Hour, "node-a")
		d := w.h.detectorWith(cfg, nil)
		goneAt := w.run(d, 24*time.Hour, func(el time.Duration) {})
		if len(goneAt) != 0 {
			t.Errorf("single node silent 24h: gone = %v", goneAt)
		}
	})
}

// Retirement then return: node-a silent past the retirement threshold, then
// back with api->db only (api->cache really gone during maintenance). Its
// edges are reported gone once at retirement; api->db's return re-arms it
// silently (no "reappeared" event).
func TestReviewProbeDriftRetireReturn(t *testing.T) {
	for _, ga := range []time.Duration{30 * time.Minute, 24 * time.Hour} {
		t.Run(fmt.Sprint(ga), func(t *testing.T) {
			cfg := DriftConfig{GoneAfter: ga, Warmup: 0}
			w := newProbeWorld(t, "", false)
			w.warm(2*time.Hour, "node-a", "node-b", "node-c")
			d := w.h.detectorWith(cfg, nil)
			outage := 8 * time.Hour
			if ga == 24*time.Hour {
				outage = 4 * 24 * time.Hour
			}
			goneAt := w.run(d, outage+outage/2, func(el time.Duration) {
				w.see("web", "redis", 6379)
				w.see("worker", "q", 5672)
				w.beat("node-b")
				w.beat("node-c")
				if el >= outage {
					w.see("api", "db", 5432)
					w.beat("node-a")
				}
			})
			t.Logf("GoneAfter %s, node-a out %s then back: gone at %v", ga, outage, goneAt)
			want := []string{w.edge("api", "cache")}
			if ga == 30*time.Minute { // retired (6h) before the return
				want = append(want, w.edge("api", "db"))
			}
			if goneKeys(goneAt) != strings.Join(sortedCopy(want), ",") {
				t.Errorf("gone = %v, want %v", goneAt, want)
			}
			for _, e := range w.h.feed() {
				if e.Kind != storage.DriftDisappeared {
					t.Errorf("unexpected %s event for %s", e.Kind, e.ConnectionID)
				}
			}
		})
	}
}

// Idle spend: in-use services on a node whose agent has been dead for days.
// Per node: the node is gone, its services excluded. In a CLUSTER_NAME
// group: flagged, but low confidence, out of the total, under a note naming
// the silent agent and since when. (v3.1.0-rc: medium, in the total.)
func TestReviewProbeIdleDeadAgent(t *testing.T) {
	for _, g := range []string{"", "Kubernetes"} {
		for _, deadDays := range []int{8, 12} {
			t.Run(fmt.Sprintf("group=%q dead=%dd", g, deadDays), func(t *testing.T) {
				ts, _ := newHistoryTestService(t, true)
				ctx := context.Background()
				now := time.Now()
				na, nb := "node-a", "node-b"
				if g != "" {
					na, nb = g, g
				}
				dead := now.Add(-time.Duration(deadDays) * day)
				billing := &storage.Service{ID: "shop/billing", Name: "billing", Type: "web_server", Node: na}
				gw := &storage.Service{ID: "shop/gw", Name: "gw", Type: "web_server", Node: na}
				web := &storage.Service{ID: "shop/web", Name: "web", Type: "web_server", Node: nb}
				cl := &storage.Service{ID: "shop/client", Name: "client", Type: "web_server", Node: nb}
				gwBill := &storage.Connection{ID: "gw->billing", SourceID: gw.ID, TargetID: billing.ID, Port: 9000}
				clWeb := &storage.Connection{ID: "client->web", SourceID: cl.ID, TargetID: web.ID, Port: 80}
				spans := []HistorySpan{
					{Service: billing, From: now.Add(-20 * day), To: dead},
					{Service: gw, From: now.Add(-20 * day), To: dead},
					{Service: web, From: now.Add(-20 * day), To: now},
					{Service: cl, From: now.Add(-20 * day), To: now},
					{Connection: gwBill, From: now.Add(-20 * day), To: dead},
					{Connection: clWeb, From: now.Add(-20 * day), To: now},
					{Node: "node-a", Group: g, From: now.Add(-20 * day), To: dead},
					{Node: "node-b", Group: g, From: now.Add(-20 * day), To: now},
				}
				if err := ts.ImportHistory(ctx, spans); err != nil {
					t.Fatal(err)
				}
				for _, s := range []*storage.Service{web, cl} {
					cp := *s
					if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
						t.Fatal(err)
					}
				}
				if err := ts.AddConnection(ctx, &storage.Connection{ID: clWeb.ID, SourceID: cl.ID, TargetID: web.ID, Port: 80, LastSeen: now}); err != nil {
					t.Fatal(err)
				}
				if g != "" {
					ts.NoteAgentNode(g, "node-b")
				}
				ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
				r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
				if err != nil {
					t.Fatal(err)
				}
				flagged := map[string]IdleServiceItem{}
				for _, it := range r.Items {
					t.Logf("FLAGGED %s cat=%s conf=%s in_total=%v unobserved=%ds", it.ID, it.Category, it.IdleConfidence, it.InTotal, it.Evidence.UnobservedSeconds)
					flagged[it.ID] = it
				}
				t.Logf("totals=%+v notes=%q", r.Totals, r.Notes)
				if g == "" {
					for _, ex := range r.Excluded {
						if (ex.ID == billing.ID || ex.ID == gw.ID) && ex.Reason != IdleExcludedNodeGone {
							t.Errorf("%s excluded as %s, want %s", ex.ID, ex.Reason, IdleExcludedNodeGone)
						}
					}
					if _, ok := flagged[billing.ID]; ok {
						t.Errorf("billing on the gone node flagged")
					}
					if len(r.SilentAgents) != 0 {
						t.Errorf("silent agents listed for a per-node install: %+v", r.SilentAgents)
					}
					return
				}
				if len(flagged) != 4 {
					t.Fatalf("flagged %d, want all 4 (low)", len(flagged))
				}
				for id, it := range flagged {
					if it.IdleConfidence != ConfidenceLow || it.InTotal {
						t.Errorf("%s: conf=%s in_total=%v, want low / not in total", id, it.IdleConfidence, it.InTotal)
					}
				}
				if r.Totals.Monthly != 0 || r.Totals.NotInTotalMonthly != 300 {
					t.Errorf("totals = %+v, want 0 in the total and $300 not in it", r.Totals)
				}
				want := fmt.Sprintf("the agent on node-a stopped reporting %dd ago", deadDays)
				if !strings.Contains(r.Notes[0], want) {
					t.Errorf("first note = %q, want it to contain %q", r.Notes[0], want)
				}
				if len(r.SilentAgents) != 1 || r.SilentAgents[0].Node != "node-a" || r.SilentAgents[0].Group != g {
					t.Errorf("silent_agents = %+v", r.SilentAgents)
				}
			})
		}
	}
}

// A1 (fourth review): node-a's agent was down from -5d until 2h ago and is
// reporting again. billing on node-a is called daily by a cron on node-a,
// unseen during the outage. Per node, billing is held back (the outage is
// not silence). In a CLUSTER_NAME group the outage stopped holding the group
// back after an hour, so billing looks quiet: it is flagged, but low
// confidence, out of the total, with the returned agent named - even though
// that agent is reporting now. (Before: orphan, in the total, $150/mo, no
// note.)
func TestReviewR3ReturnedMember(t *testing.T) {
	for _, g := range []string{"", "Kubernetes"} {
		t.Run("group="+g, func(t *testing.T) {
			ts, _ := newHistoryTestService(t, true)
			ctx := context.Background()
			now := time.Now()
			na, nb := "node-a", "node-b"
			if g != "" {
				na, nb = g, g
			}
			down, back := now.Add(-5*day), now.Add(-2*time.Hour)
			billing := &storage.Service{ID: "10.0.1.10/billing", Name: "billing", Type: "web_server", Node: na}
			cron := &storage.Service{ID: "10.0.1.11/cron", Name: "cron", Type: "application", Node: na}
			web := &storage.Service{ID: "10.0.2.10/web", Name: "web", Type: "web_server", Node: nb}
			cl := &storage.Service{ID: "10.0.2.11/client", Name: "client", Type: "application", Node: nb}
			var spans []HistorySpan
			for d := 20; d >= 5; d-- { // cron->billing daily until the outage
				at := now.Add(-time.Duration(d)*day + time.Hour)
				if at.After(down) {
					break
				}
				spans = append(spans, HistorySpan{Connection: &storage.Connection{ID: "cron->billing", SourceID: cron.ID, TargetID: "10.0.1.10:9000", Port: 9000}, From: at, To: at.Add(10 * time.Minute)})
			}
			spans = append(spans,
				HistorySpan{Service: billing, From: now.Add(-20 * day), To: down},
				HistorySpan{Service: billing, From: back, To: now},
				HistorySpan{Service: cron, From: now.Add(-20 * day), To: down},
				HistorySpan{Service: cron, From: back, To: now},
				HistorySpan{Service: web, From: now.Add(-20 * day), To: now},
				HistorySpan{Service: cl, From: now.Add(-20 * day), To: now},
				HistorySpan{Connection: &storage.Connection{ID: "client->web", SourceID: cl.ID, TargetID: web.ID, Port: 80}, From: now.Add(-20 * day), To: now},
				HistorySpan{Node: "node-a", Group: g, From: now.Add(-20 * day), To: down},
				HistorySpan{Node: "node-a", Group: g, From: back, To: now},
				HistorySpan{Node: "node-b", Group: g, From: now.Add(-20 * day), To: now},
			)
			if err := ts.ImportHistory(ctx, spans); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*storage.Service{billing, cron, web, cl} {
				cp := *s
				if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
					t.Fatal(err)
				}
			}
			if err := ts.AddConnection(ctx, &storage.Connection{ID: "client->web", SourceID: cl.ID, TargetID: web.ID, Port: 80, LastSeen: now}); err != nil {
				t.Fatal(err)
			}
			if g != "" {
				ts.NoteAgentNode(g, "node-a")
				ts.NoteAgentNode(g, "node-b")
			}
			ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
			r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
			if err != nil {
				t.Fatal(err)
			}
			item := findItem(r, billing.ID)
			if g == "" {
				if item != nil {
					t.Fatalf("per node: billing flagged %s/%s", item.Category, item.IdleConfidence)
				}
				if len(r.SilentAgents) != 0 {
					t.Errorf("per node: silent agents %+v", r.SilentAgents)
				}
				return
			}
			if item == nil {
				t.Fatalf("grouped: billing not flagged: %+v", r.Excluded)
			}
			t.Logf("billing %s/%s in_total=%v reason=%q; silent=%+v; note=%q", item.Category, item.IdleConfidence, item.InTotal, item.NotInTotalReason, r.SilentAgents, r.Notes[0])
			if item.IdleConfidence != ConfidenceLow || item.InTotal || item.NotInTotalReason != NotInTotalSilentAgent {
				t.Errorf("billing conf=%s in_total=%v reason=%q, want low / false / silent_agent", item.IdleConfidence, item.InTotal, item.NotInTotalReason)
			}
			if len(r.SilentAgents) != 1 || r.SilentAgentsTotal != 1 || r.SilentAgentsStillOut != 0 || r.SilentNotes != 1 {
				t.Fatalf("silent_agents = %+v (total %d, still out %d, notes %d), want node-a, back", r.SilentAgents, r.SilentAgentsTotal, r.SilentAgentsStillOut, r.SilentNotes)
			}
			a := r.SilentAgents[0]
			if a.Node != "node-a" || a.Intermittent || a.ReturnedAt == nil ||
				a.SilentSince.Sub(down).Abs() > time.Minute || a.ReturnedAt.Sub(back).Abs() > time.Minute {
				t.Errorf("silent agent = %+v, want node-a out from %s, back at %s", a, down, back)
			}
			if !strings.Contains(r.Notes[0], "the agent on node-a was not reporting from") || !strings.Contains(r.Notes[0], "UTC (4d)") ||
				!strings.Contains(r.Notes[0], "The agent is reporting again") {
				t.Errorf("note = %q", r.Notes[0])
			}
			if r.Totals.Monthly != 0 || r.Totals.NotInTotalByReason[NotInTotalSilentAgent] != r.Totals.NotInTotalMonthly {
				t.Errorf("totals = %+v", r.Totals)
			}
			// Exports say why, and the CSV lets a spreadsheet leave it out.
			mdText := RenderIdleSpendMarkdown(r)
			if !strings.Contains(mdText, "Not in the total: $150/month (an agent in its group was not reporting: $150)") ||
				!strings.Contains(mdText, "$150 (not in total: an agent in its group was not reporting)") ||
				strings.Contains(mdText, "current-window") {
				t.Errorf("markdown:\n%s", mdText)
			}
			csvOut, err := RenderIdleSpendCSV(r)
			if err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSpace(string(csvOut)), "\n")
			if !strings.Contains(rows[0], ",in_total,not_in_total_reason,") || !strings.Contains(rows[1], ",false,silent_agent,") {
				t.Errorf("csv:\n%s", csvOut)
			}
			for _, n := range r.Nodes {
				if n.Node == g && (n.IdleMonthly != 0 || n.IdleMonthlyNotInTotal != 150) {
					t.Errorf("node summary %+v, want 0 in total and 150 out", n)
				}
			}
		})
	}
}

// A flapping member (one heartbeat every 20 minutes for two days, now
// steady again) also marks its group's candidates low and out of the
// total, reported as intermittent.
func TestIdleFlappingMemberDowngrades(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()
	now := time.Now()
	const g = "Kubernetes"
	billing := &storage.Service{ID: "10.0.1.10/billing", Name: "billing", Type: "web_server", Node: g}
	spans := []HistorySpan{
		{Service: billing, From: now.Add(-20 * day), To: now},
		{Node: "node-b", Group: g, From: now.Add(-20 * day), To: now},
		{Node: "node-a", Group: g, From: now.Add(-20 * day), To: now.Add(-6 * day)},
		{Node: "node-a", Group: g, From: now.Add(-4 * day), To: now},
	}
	for at := now.Add(-6*day + 20*time.Minute); at.Before(now.Add(-4 * day)); at = at.Add(20 * time.Minute) {
		spans = append(spans, HistorySpan{Node: "node-a", Group: g, From: at, To: at})
	}
	if err := ts.ImportHistory(ctx, spans); err != nil {
		t.Fatal(err)
	}
	cp := *billing
	if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
		t.Fatal(err)
	}
	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
	r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	item := findItem(r, billing.ID)
	if item == nil {
		t.Fatalf("billing not flagged: %+v", r.Excluded)
	}
	if item.IdleConfidence != ConfidenceLow || item.InTotal || item.NotInTotalReason != NotInTotalSilentAgent {
		t.Errorf("billing conf=%s in_total=%v reason=%q", item.IdleConfidence, item.InTotal, item.NotInTotalReason)
	}
	if len(r.SilentAgents) != 1 || !r.SilentAgents[0].Intermittent || r.SilentAgents[0].ReturnedAt == nil {
		t.Fatalf("silent_agents = %+v, want node-a, intermittent, returned", r.SilentAgents)
	}
	if !strings.Contains(r.Notes[0], "the agent on node-a reported only intermittently from") {
		t.Errorf("note = %q", r.Notes[0])
	}
}

// Decommissioned node-a with Kubernetes-named edges, grouped and not, back
// after 12h: its edges are reported gone once, and its return announces
// nothing.
func TestReviewR3K8sDecommissionAndReturn(t *testing.T) {
	for _, g := range []string{"", "Kubernetes"} {
		t.Run("group="+g, func(t *testing.T) {
			w := newProbeWorld(t, g, true)
			w.warm(2*time.Hour, "node-a", "node-b", "node-c")
			d := w.h.detectorWith(DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}, nil)
			goneAt := w.run(d, 18*time.Hour, func(el time.Duration) {
				w.see("web", "redis", 6379)
				w.see("worker", "q", 5672)
				w.beat("node-b")
				w.beat("node-c")
				if el >= 12*time.Hour {
					w.see("api", "db", 5432)
					w.beat("node-a")
				}
			})
			t.Logf("group=%q: gone at %v; %d events", g, goneAt, len(w.h.feed()))
			want := strings.Join(sortedCopy([]string{w.edge("api", "db"), w.edge("api", "cache")}), ",")
			if goneKeys(goneAt) != want || len(w.h.feed()) != 2 {
				t.Errorf("gone = %v, feed %v; want both api edges once and nothing else", goneAt, feedIDs(w.h.feed()))
			}
		})
	}
}

// Known limit (documented): Kubernetes-named edges are attributed to the
// node on the source's service row. api has replicas on node-a and node-b,
// its row says node-b, only node-a's replica calls cache: when node-a's
// agent dies, api->cache is measured against node-b and reported gone after
// GoneAfter.
func TestReviewR3ReplicaAttribution(t *testing.T) {
	w := newProbeWorld(t, "", true)
	ctx := context.Background()
	if err := w.h.store.Services().Upsert(ctx, &storage.Service{ID: w.ids["api"], Name: "api", Node: "node-b"}); err != nil {
		t.Fatal(err)
	}
	w.warm(2*time.Hour, "node-a", "node-b", "node-c")
	d := w.h.detectorWith(DriftConfig{GoneAfter: 30 * time.Minute, Warmup: 0}, nil)
	goneAt := w.run(d, 3*time.Hour, func(el time.Duration) {
		w.see("api", "db", 5432) // node-b's replica keeps calling db
		w.see("web", "redis", 6379)
		w.see("worker", "q", 5672)
		w.beat("node-b")
		w.beat("node-c")
	})
	t.Logf("replica attribution: gone at %v", goneAt)
	if goneKeys(goneAt) != w.edge("api", "cache") {
		t.Errorf("gone = %v; the documented limit is api->cache reported gone", goneAt)
	}
}

// r4span is a heartbeat interval as durations before now.
type r4span struct{ from, to time.Duration }

// r4Idle (from the fifth review): group "K" (or node-a when group is "")
// carries a genuinely idle service legacy and a live ingress->web; members
// heartbeat as given.
func r4Idle(t *testing.T, group string, members map[string][]r4span) *IdleSpendReport {
	t.Helper()
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()
	now := time.Now()
	node := group
	if group == "" {
		node = "node-a"
	}
	legacy := &storage.Service{ID: "Deploy:prod/legacy", Name: "legacy", Type: "web_server", Node: node}
	web := &storage.Service{ID: "Deploy:prod/web", Name: "web", Type: "web_server", Node: node}
	ing := &storage.Service{ID: "Deploy:prod/ingress", Name: "ingress", Type: "web_server", Node: node}
	old := &storage.Connection{ID: "ingress->legacy", SourceID: ing.ID, TargetID: legacy.ID, Port: 8080}
	live := &storage.Connection{ID: "ingress->web", SourceID: ing.ID, TargetID: web.ID, Port: 8080}
	spans := []HistorySpan{
		{Service: legacy, From: now.Add(-29 * day), To: now},
		{Service: web, From: now.Add(-29 * day), To: now},
		{Service: ing, From: now.Add(-29 * day), To: now},
		{Connection: old, From: now.Add(-29 * day), To: now.Add(-20 * day)},
		{Connection: live, From: now.Add(-29 * day), To: now},
	}
	for m, ss := range members {
		for _, s := range ss {
			spans = append(spans, HistorySpan{Node: m, Group: group, From: now.Add(-s.from), To: now.Add(-s.to)})
		}
	}
	if err := ts.ImportHistory(ctx, spans); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*storage.Service{legacy, web, ing} {
		cp := *s
		if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
			t.Fatal(err)
		}
	}
	if err := ts.AddConnection(ctx, &storage.Connection{ID: live.ID, SourceID: ing.ID, TargetID: web.ID, Port: 8080, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if group != "" {
		for m := range members {
			ts.NoteAgentNode(group, m)
		}
	}
	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})
	r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// F1/F2 (fifth review): only an agent's own failure is an outage. A backend
// outage silences every member at once and warns nobody; one member out
// while the others report does, also when it began before the window.
func TestReviewR4Outages(t *testing.T) {
	d := func(x float64) time.Duration { return time.Duration(x * float64(day)) }
	h := func(x float64) time.Duration { return time.Duration(x * float64(time.Hour)) }
	full := []r4span{{d(29), 0}}
	gap := func(at, length time.Duration) []r4span { return []r4span{{d(29), at}, {at - length, 0}} }
	cases := []struct {
		name    string
		group   string
		members map[string][]r4span
		silent  string // the agent listed, "" for none
	}{
		{"healthy", "K", map[string][]r4span{"a": full, "b": full, "c": full}, ""},
		{"backend down 2h for everyone 5d ago", "K", map[string][]r4span{"a": gap(d(5), h(2)), "b": gap(d(5), h(2)), "c": gap(d(5), h(2))}, ""},
		{"backend down 50m for everyone", "K", map[string][]r4span{"a": gap(d(5), h(0.83)), "b": gap(d(5), h(0.83))}, ""},
		{"single-member group, backend down 2h", "K", map[string][]r4span{"a": gap(d(5), h(2))}, ""},
		{"a joined 25d ago (no earlier heartbeat)", "K", map[string][]r4span{"a": {{d(25), 0}}, "b": {{d(35), 0}}}, ""},
		{"a: old 3d outage + recent 2h blip", "K", map[string][]r4span{"a": {{d(29), d(20)}, {d(17), d(1)}, {d(1) - h(2), 0}}, "b": full}, "a"},
		{"a joined mid-window, healthy since", "K", map[string][]r4span{"a": {{d(10), 0}}, "b": full}, ""},
		{"a left (scaled in) 10d ago", "K", map[string][]r4span{"a": {{d(29), d(10)}}, "b": full}, "a"},
		{"ungrouped 2h gap", "", map[string][]r4span{"node-a": gap(d(5), h(2))}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := r4Idle(t, c.group, c.members)
			it := findItem(r, "Deploy:prod/legacy")
			if it == nil {
				t.Fatalf("legacy not flagged: %+v", r.Excluded)
			}
			var listed []string
			for _, a := range r.SilentAgents {
				listed = append(listed, a.Node)
			}
			t.Logf("legacy %s in_total=%v; silent %v", it.IdleConfidence, it.InTotal, listed)
			if c.silent == "" {
				if len(listed) != 0 || !it.InTotal || it.IdleConfidence == ConfidenceLow {
					t.Errorf("want no warning: silent=%v in_total=%v conf=%s", listed, it.InTotal, it.IdleConfidence)
				}
				return
			}
			if len(listed) != 1 || listed[0] != c.silent || it.InTotal || it.IdleConfidence != ConfidenceLow {
				t.Errorf("want %s listed and legacy low/out: silent=%v in_total=%v conf=%s", c.silent, listed, it.InTotal, it.IdleConfidence)
			}
		})
	}
}

// F2: an outage that began before the evidence window and ended inside it
// is reported (heartbeats are no longer clipped to the window).
func TestReviewR4OutageAcrossWindowStart(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	since := now.Add(-29 * day)
	c := newObservationCoverage(5*time.Minute, 5*time.Minute, 7*day)
	c.now = func() time.Time { return now }
	c.setHeartbeats([]storage.NodeInterval{
		{Node: "a", Group: "K", FirstSeen: now.Add(-60 * day), LastSeen: now.Add(-31 * day)},
		{Node: "a", Group: "K", FirstSeen: now.Add(-27 * day), LastSeen: now},
		{Node: "b", Group: "K", FirstSeen: now.Add(-60 * day), LastSeen: now},
	})
	outs := c.memberOutages(since, now)
	if len(outs) != 1 || outs[0].Node != "a" || !outs[0].From.Equal(now.Add(-31*day)) || !outs[0].To.Equal(now.Add(-27*day)) {
		t.Errorf("outages = %+v, want a from -31d to -27d", outs)
	}
	// End to end: the Idle-spend coverage keeps heartbeats from before the window.
	in := idleInputs{now: now, maxGap: 5 * time.Minute, coverageStart: since, retireAfter: 7 * day, historyNodes: []storage.NodeInterval{
		{Node: "a", Group: "K", FirstSeen: now.Add(-60 * day), LastSeen: now.Add(-31 * day)},
		{Node: "a", Group: "K", FirstSeen: now.Add(-27 * day), LastSeen: now},
		{Node: "b", Group: "K", FirstSeen: now.Add(-60 * day), LastSeen: now},
	}}
	if outs := buildIdleCoverage(in, func(string) string { return "" }).memberOutages(since, now); len(outs) != 1 || outs[0].Node != "a" {
		t.Errorf("idle coverage outages = %+v, want a", outs)
	}
}

// The flapping rule at and around its threshold (a member up `up` of every
// 80 minutes, another steady): evaluated per 5-minute step over the
// trailing two hours, so a member reporting ~56% of the time (with the
// heartbeat grace) is listed as intermittent and one at ~69% is not.
func TestReviewR4FlapUnit(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	start := now.Add(-10 * day)
	for _, c := range []struct {
		up     time.Duration
		listed bool
	}{{25 * time.Minute, true}, {30 * time.Minute, true}, {35 * time.Minute, true}, {40 * time.Minute, true}, {50 * time.Minute, false}} {
		period := 80 * time.Minute
		beats := []storage.NodeInterval{{Node: "a", Group: "K", FirstSeen: start, LastSeen: now}}
		for at := start; at.Before(now); at = at.Add(period) {
			end := at.Add(c.up)
			if end.After(now) {
				end = now
			}
			beats = append(beats, storage.NodeInterval{Node: "c", Group: "K", FirstSeen: at, LastSeen: end})
		}
		cov := newObservationCoverage(5*time.Minute, 5*time.Minute, 7*day)
		cov.now = func() time.Time { return now }
		cov.setHeartbeats(beats)
		outs := cov.memberOutages(now.Add(-7*day), now)
		if listed := len(outs) == 1 && outs[0].Node == "c" && outs[0].Intermittent; listed != c.listed || (!c.listed && len(outs) != 0) {
			t.Errorf("up %s of %s: outages %+v, want listed=%v", c.up, period, outs, c.listed)
		}
	}
}

// F4: with more out-agents than silent_agents holds, the counts and the
// number of leading notes still describe all of them.
func TestIdleSilentAgentCountsBeyondTheCap(t *testing.T) {
	now := idleNow
	w := newIdleWorld()
	w.svc("10.0.0.1/a", "a", "web_server", "G0")
	for g := 0; g < 3; g++ {
		group := fmt.Sprintf("G%d", g)
		w.in.historyNodes = append(w.in.historyNodes, storage.NodeInterval{Node: "steady", Group: group, FirstSeen: ago(14 * day), LastSeen: now})
		for i := 0; i < 10; i++ {
			last := ago(2 * day)
			w.in.historyNodes = append(w.in.historyNodes, storage.NodeInterval{Node: fmt.Sprintf("n%d", i), Group: group, FirstSeen: ago(14 * day), LastSeen: last})
			if i%2 == 0 { // back again
				w.in.historyNodes = append(w.in.historyNodes, storage.NodeInterval{Node: fmt.Sprintf("n%d", i), Group: group, FirstSeen: ago(day), LastSeen: now})
			}
		}
	}
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 100}, IdleSpendQuery{})
	if len(r.SilentAgents) != maxIdleSilentAgents || r.SilentAgentsTotal != 30 || r.SilentAgentsStillOut != 15 || r.SilentNotes != 3 {
		t.Errorf("listed %d, total %d, still out %d, notes %d; want 20, 30, 15, 3", len(r.SilentAgents), r.SilentAgentsTotal, r.SilentAgentsStillOut, r.SilentNotes)
	}
	for i := 0; i < r.SilentNotes; i++ {
		if !strings.HasPrefix(r.Notes[i], "In CLUSTER_NAME group") {
			t.Errorf("note %d = %q", i, r.Notes[i])
		}
	}
}
