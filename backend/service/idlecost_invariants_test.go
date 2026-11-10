package service

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Invariant tests for idle spend. These check properties that must hold for
// any topology rather than snapshots of one, plus the independent review's
// confirmed repros as regressions (named TestReview*).

// sumByNode adds up the attributed figures per node (service-rule items on
// node-less endpoints have no node and are skipped).
func sumByNode(r *IdleSpendReport) map[string]float64 {
	out := map[string]float64{}
	for _, it := range r.Items {
		if it.Cost != nil && it.Node != "" {
			out[it.Node] += it.Cost.Monthly
		}
	}
	return out
}

func checkNodeBudget(t *testing.T, r *IdleSpendReport, label string) {
	t.Helper()
	sums := sumByNode(r)
	for _, n := range r.Nodes {
		if n.Monthly == nil {
			if sums[n.Node] > 0 {
				// Only service-rule prices may stand on an unpriced node.
				for _, it := range r.Items {
					if it.Node == n.Node && it.Cost != nil && it.Cost.PriceSource != PriceSourceServiceRule {
						t.Errorf("%s: %s is unpriced but %s got a %s figure", label, n.Node, it.ID, it.Cost.PriceSource)
					}
				}
			}
			continue
		}
		if sums[n.Node] > *n.Monthly+0.001 {
			t.Errorf("%s: node %s attributed %.2f > its price %.2f", label, n.Node, sums[n.Node], *n.Monthly)
		}
	}
}

// randomWorld builds a messy topology: several nodes, mixed types, live and
// past edges, stale services, metrics-only nodes, service rules.
func randomWorld(rng *rand.Rand) (*idleWorld, IdleCostConfig) {
	w := newIdleWorld()
	types := []string{"web_server", "database", "cache", "application", "monitoring", "message_queue", "proxy"}
	nNodes := 1 + rng.Intn(5)
	var ids []string
	for n := 0; n < nNodes; n++ {
		node := fmt.Sprintf("n%d", n)
		if rng.Intn(3) > 0 {
			w.node(node)
		}
		for i := 0; i < rng.Intn(8); i++ {
			id := fmt.Sprintf("10.%d.0.%d/s%d", n, i, i)
			w.svc(id, fmt.Sprintf("s%d-%d", n, i), types[rng.Intn(len(types))], node)
			ids = append(ids, id)
		}
		for i := 0; i < rng.Intn(4); i++ {
			w.in.stale = append(w.in.stale, storage.ServiceInterval{
				ServiceID: fmt.Sprintf("10.%d.9.%d/old%d", n, i, i), Name: fmt.Sprintf("old%d-%d", n, i),
				Type: types[rng.Intn(len(types))], Node: node, FirstSeen: ago(14 * day), LastSeen: ago(time.Duration(4+rng.Intn(9)) * day),
			})
		}
	}
	ids = append(ids, "203.0.113.1")
	w.svc("203.0.113.1", "external", "unknown", "")
	w.in.services[len(w.in.services)-1].Tech = "External"
	if len(ids) > 1 {
		for i := 0; i < rng.Intn(2*len(ids)); i++ {
			a, b := ids[rng.Intn(len(ids))], ids[rng.Intn(len(ids))]
			if a == b {
				continue
			}
			if rng.Intn(2) == 0 {
				w.liveEdge(a, b, uint16(1000+rng.Intn(5)))
			} else {
				w.pastEdge(a, b, uint16(1000+rng.Intn(5)), ago(time.Duration(rng.Intn(13*24))*time.Hour))
			}
		}
	}
	cfg := IdleCostConfig{Warmup: -1}
	if rng.Intn(2) == 0 {
		cfg.DefaultNodeMonthly = float64(50 + rng.Intn(500))
	}
	if rng.Intn(2) == 0 {
		cfg.NodePrices = []PriceRule{{Pattern: fmt.Sprintf("n%d", rng.Intn(nNodes)), Monthly: float64(10 + rng.Intn(900))}}
	}
	if rng.Intn(2) == 0 && len(ids) > 1 {
		cfg.ServicePrices = []PriceRule{{Pattern: "s*-1", Monthly: float64(rng.Intn(400))}, {Pattern: "old*-0", Monthly: float64(rng.Intn(400))}}
	}
	return w, cfg
}

func TestInvariantNodeAttributionNeverExceedsPrice(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 500; i++ {
		w, cfg := randomWorld(rng)
		r := w.run(cfg, IdleSpendQuery{AssumeNodeMonthly: float64(rng.Intn(3) * 100)})
		checkNodeBudget(t, r, fmt.Sprintf("world %d", i))
	}
}

func TestInvariantLiveTrafficIsNeverFlagged(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		w, cfg := randomWorld(rng)
		r := w.run(cfg, IdleSpendQuery{})
		flagged := map[string]bool{}
		for _, it := range r.Items {
			flagged[it.ID] = true
		}
		meta := map[string]storage.Service{}
		for _, s := range w.in.services {
			meta[s.ID] = s
		}
		for _, it := range r.Items {
			for _, c := range w.in.connections {
				if it.Category == IdleCategoryStale && (c.SourceID == it.ID || c.TargetID == it.ID) {
					t.Errorf("world %d: %s reported as not seen while endpoint of live connection %s", i, it.ID, c.ID)
				}
				if c.TargetID != it.ID || c.SourceID == it.ID {
					continue
				}
				if !flagged[c.SourceID] && !isIgnoredCaller(meta[c.SourceID], meta[it.ID]) {
					t.Errorf("world %d: %s flagged (%s) though %s (not flagged) talks to it live", i, it.ID, it.Category, c.SourceID)
				}
			}
		}
	}
}

// Long-lived pool: both endpoints aged out of current state, the connection
// still carries traffic. Reviewer repro: both were listed as stale at $300.
func TestReviewPhantomEndpointsAreNotFlagged(t *testing.T) {
	w := busyNode(newIdleWorld())
	w.in.connections = append(w.in.connections, storage.Connection{ID: "pool", SourceID: "10.0.0.5/worker", TargetID: "10.0.0.6:5432", Port: 5432, LastSeen: idleNow, BytesSent: 1 << 30})
	w.in.historyEdges = append(w.in.historyEdges, storage.ConnectionInterval{ConnectionID: "pool", SourceID: "10.0.0.5/worker", TargetID: "10.0.0.6:5432", Port: 5432, FirstSeen: ago(14 * day), LastSeen: idleNow})
	w.in.historyServices = append(w.in.historyServices,
		storage.ServiceInterval{ServiceID: "10.0.0.5/worker", Name: "worker", Type: "application", Node: "n1", FirstSeen: ago(14 * day), LastSeen: ago(8 * day)},
		storage.ServiceInterval{ServiceID: "10.0.0.6:5432", Name: "orders-db", Type: "database", Node: "n1", FirstSeen: ago(14 * day), LastSeen: ago(8 * day)})
	w.in.stale = []storage.ServiceInterval{
		{ServiceID: "10.0.0.5/worker", Name: "worker", Type: "application", Node: "n1", LastSeen: ago(8 * day)},
		{ServiceID: "10.0.0.6:5432", Name: "orders-db", Type: "database", Node: "n1", LastSeen: ago(8 * day)},
	}
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 300}, IdleSpendQuery{})
	if len(r.Items) != 0 {
		t.Fatalf("live pool endpoints flagged: %+v", r.Items)
	}
	for _, id := range []string{"10.0.0.5/worker", "10.0.0.6:5432"} {
		if ex := findExcluded(r, id); ex == nil || ex.Reason != IdleExcludedLiveTraffic {
			t.Errorf("%s: excluded = %+v, want live_traffic", id, ex)
		}
	}
}

// A pool idle for a while but last used within QuietAfter still counts.
func TestRecentHistoryEdgeProtectsStaleEndpoint(t *testing.T) {
	w := busyNode(newIdleWorld())
	w.pastEdge("10.0.0.5/worker", "10.0.0.6:5432", 5432, ago(20*time.Hour))
	w.in.stale = []storage.ServiceInterval{{ServiceID: "10.0.0.6:5432", Name: "orders-db", Type: "database", Node: "n1", LastSeen: ago(8 * day)}}
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 300}, IdleSpendQuery{})
	if findItem(r, "10.0.0.6:5432") != nil {
		t.Errorf("endpoint of an edge seen 20h ago flagged as not seen")
	}
}

// Reviewer repro: several stale services on one node each claimed n=live+1;
// a $300 node was attributed $600 and a metrics-only $100 node $300.
func TestReviewStaleDoNotDoubleCount(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.7.1/a", "a", "web_server", "n7").
		svc("10.0.7.2/b", "b", "web_server", "n7").
		node("n7").node("n8")
	for i, n := range []string{"n7", "n7", "n7", "n8", "n8", "n8"} {
		w.in.stale = append(w.in.stale, storage.ServiceInterval{ServiceID: fmt.Sprintf("10.0.9.%d/s%d", i, i), Name: fmt.Sprintf("s%d", i), Node: n, LastSeen: ago(10 * day)})
	}
	r := w.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n7", Monthly: 300}, {Pattern: "n8", Monthly: 100}}}, IdleSpendQuery{})
	checkNodeBudget(t, r, "stale")
	sums := sumByNode(r)
	if sums["n7"] > 300 || sums["n8"] > 100 {
		t.Errorf("sums = %v", sums)
	}
	// One denominator per node: every n7 item shares the same split.
	for _, it := range r.Items {
		if it.Node == "n7" && (it.Cost == nil || it.Cost.SharedWith != 5) {
			t.Errorf("%s: cost = %+v, want shared with 5 (2 live + 3 stale)", it.ID, it.Cost)
		}
	}
}

// Rounding must not push a split over the price (4000 x $0.03 > $100).
func TestReviewRoundingNeverExceedsPrice(t *testing.T) {
	w := newIdleWorld().node("n1")
	name := func(i int) string { return fmt.Sprintf("10.1.%d.%d/s%06d", i/250, i%250, 4000-i) }
	w.svc(name(0), "head", "web_server", "n1")
	for i := 1; i < 4000; i++ {
		w.svc(name(i), fmt.Sprintf("s%d", i), "web_server", "n1")
		w.liveEdge(name(i-1), name(i), 80)
	}
	start := time.Now()
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 100}, IdleSpendQuery{})
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("4000-service chain took %v", d)
	}
	checkNodeBudget(t, r, "chain")
	if r.Totals.Monthly > 100 {
		t.Errorf("total %.2f > node price 100", r.Totals.Monthly)
	}
}

// Reviewer repro: an actively used prod-db with its own price did not stop
// the node being declared fully idle ($500 whole node, high).
func TestReviewServicePricedActiveBlocksFullyIdle(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.9.1/prod-db", "prod-db", "database", "n9").
		svc("10.0.9.2/oldapp", "oldapp", "web_server", "n9").
		liveEdge("10.0.0.1/gw", "10.0.9.1/prod-db", 5432).
		node("n9")
	r := w.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n9", Monthly: 500}}, ServicePrices: []PriceRule{{Pattern: "prod-db", Monthly: 200}}}, IdleSpendQuery{})
	checkNodeBudget(t, r, "carve-out")
	for _, n := range r.Nodes {
		if n.Node == "n9" && n.FullyIdle {
			t.Errorf("n9 declared fully idle while prod-db is in use")
		}
	}
	for _, note := range r.Notes {
		if strings.Contains(note, "n9") && strings.Contains(note, "whole node") {
			t.Errorf("whole-node note on n9: %s", note)
		}
	}
	it := findItem(r, "10.0.9.2/oldapp")
	if it == nil || it.Cost == nil || it.Cost.Monthly != 300 {
		t.Fatalf("oldapp should get what is left after prod-db's carve-out (300): %+v", it)
	}
	if findItem(r, "10.0.9.1/prod-db") != nil {
		t.Errorf("prod-db flagged")
	}
}

// Reviewer repro: a replica (outbound to its primary, nothing inbound) was
// flagged no_inbound at high confidence.
func TestReviewReplicaHeldBack(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.3/postgres", "pg-replica", "database", "n2").
		liveEdge("10.0.0.3/postgres", "10.0.0.2:5432", 5432).node("n2")
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 400}, IdleSpendQuery{})
	if findItem(r, "10.0.0.3/postgres") != nil {
		t.Fatalf("replica flagged")
	}
	if ex := findExcluded(r, "10.0.0.3/postgres"); ex == nil || ex.Reason != IdleExcludedReplica {
		t.Errorf("excluded = %+v", ex)
	}
}

// Reviewer repro: the agent on a node counted in its split and blocked the
// whole-node rule. Bare-IP host daemons likewise.
func TestReviewAgentDoesNotBlockWholeNode(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.7/legacy", "legacy", "web_server", "n2").
		svc("10.0.0.8:3306", "mysql", "database", "n2").
		svc("10.0.0.7/infralens-agent", "infralens-agent", "application", "n2").
		svc("10.0.0.7", "10.0.0.7", "unknown", "n2"). // sshd collapses to its IP
		liveEdge("10.0.0.7/legacy", "10.0.0.8:3306", 3306).
		liveEdge("10.0.0.7/infralens-agent", "10.0.0.1/gw", 8080).
		node("n2")
	r := w.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n2", Monthly: 200}}}, IdleSpendQuery{})
	for _, id := range []string{"10.0.0.7/legacy", "10.0.0.8:3306"} {
		it := findItem(r, id)
		if it == nil || it.Cost == nil || it.Cost.Monthly != 100 || it.Cost.ShareMethod != ShareWholeNode {
			t.Errorf("%s: %+v", id, it)
		}
	}
	checkNodeBudget(t, r, "agent")
}

// Outage != silence: billing is called every 12h; InfraLens stopped
// observing 6 (then 8) days ago and came back 10 minutes ago. Reviewer
// repro: flagged medium after 6 days and high after 8.
func TestReviewOutageIsNotSilence(t *testing.T) {
	for _, outage := range []time.Duration{6 * day, 8 * day} {
		w := newIdleWorld()
		// Every service's history has the same gap: nothing was recorded.
		for _, s := range []struct{ id, name, typ string }{
			{"1.2.3.4", "external", "unknown"}, {"10.0.0.1/gw", "gateway", "web_server"},
			{"10.0.0.2:5432", "postgres", "database"}, {"10.0.0.5/billing", "billing", "web_server"},
		} {
			node := "n1"
			if s.id == "1.2.3.4" {
				node = ""
			}
			w.in.services = append(w.in.services, storage.Service{ID: s.id, Name: s.name, Type: s.typ, Node: node, LastSeen: idleNow, CreatedAt: ago(30 * day)})
			w.in.historyServices = append(w.in.historyServices,
				storage.ServiceInterval{ServiceID: s.id, Name: s.name, Type: s.typ, Node: node, FirstSeen: ago(14 * day), LastSeen: ago(outage)},
				storage.ServiceInterval{ServiceID: s.id, Name: s.name, Type: s.typ, Node: node, FirstSeen: ago(10 * time.Minute), LastSeen: idleNow})
		}
		w.in.services[0].Tech = "External"
		w.in.connections = append(w.in.connections,
			storage.Connection{ID: "e1", SourceID: "1.2.3.4", TargetID: "10.0.0.1/gw", Port: 443, LastSeen: idleNow},
			storage.Connection{ID: "e2", SourceID: "10.0.0.1/gw", TargetID: "10.0.0.2:5432", Port: 5432, LastSeen: idleNow},
			storage.Connection{ID: "e3", SourceID: "10.0.0.5/billing", TargetID: "10.0.0.2:5432", Port: 5432, LastSeen: idleNow})
		w.pastEdge("10.0.0.1/gw", "10.0.0.5/billing", 8080, ago(outage+time.Hour))
		w.node("n1")

		r := w.run(IdleCostConfig{DefaultNodeMonthly: 300}, IdleSpendQuery{})
		if it := findItem(r, "10.0.0.5/billing"); it != nil {
			t.Errorf("outage %v: billing flagged %s/%s: %v", outage, it.Category, it.IdleConfidence, it.Evidence.Notes)
		}
		ex := findExcluded(r, "10.0.0.5/billing")
		if ex == nil || ex.Reason != IdleExcludedNotQuietEnough || !strings.Contains(ex.Detail, "not observing") {
			t.Errorf("outage %v: excluded = %+v", outage, ex)
		}
		if r.Evidence.UnobservedSeconds < int64((outage - time.Hour).Seconds()) {
			t.Errorf("outage %v: unobserved = %ds", outage, r.Evidence.UnobservedSeconds)
		}
	}
}

// Control for the outage test: the same silence with full coverage is
// flagged, and the observed figure equals the wall-clock one.
func TestFullCoverageSilenceIsFlagged(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.5/billing", "billing", "web_server", "n1").
		liveEdge("10.0.0.5/billing", "10.0.0.2:5432", 5432).
		pastEdge("10.0.0.1/gw", "10.0.0.5/billing", 8080, ago(8*day))
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 300}, IdleSpendQuery{})
	it := findItem(r, "10.0.0.5/billing")
	if it == nil {
		t.Fatalf("not flagged: %+v", r.Excluded)
	}
	if it.Evidence.UnobservedSeconds > 60 || it.IdleConfidence != ConfidenceMedium {
		t.Errorf("evidence = %+v conf=%s", it.Evidence, it.IdleConfidence)
	}
}

// Reviewer repro: with CLUSTER_NAME (Helm default "Kubernetes") the cluster
// was priced as one node: "$140/mo node ÷ 30 services", node rules silently
// never matched.
func TestReviewClusterGroupingNeverYieldsPerNodeFigures(t *testing.T) {
	build := func() *idleWorld {
		w := newIdleWorld().node("Kubernetes")
		w.in.groups = map[string][]string{"Kubernetes": {"node-1", "node-2", "node-3"}}
		w.svc("10.0.8.8", "external", "unknown", "")
		w.in.services[len(w.in.services)-1].Tech = "External"
		for i := 0; i < 30; i++ {
			id := fmt.Sprintf("10.0.1.%d/app%d", i, i)
			w.svc(id, fmt.Sprintf("app%d", i), "web_server", "Kubernetes")
			w.liveEdge(id, "10.0.9.9:5432", 5432)
			if i >= 3 { // three of them get no inbound
				w.liveEdge("10.0.8.8", id, 8080)
			}
		}
		return w
	}

	r := build().run(IdleCostConfig{DefaultNodeMonthly: 140, NodePrices: []PriceRule{{Pattern: "node-1", Monthly: 200}, {Pattern: "typo-node", Monthly: 1}}}, IdleSpendQuery{})
	if !r.Evidence.ClusterGrouped {
		t.Errorf("cluster grouping not detected")
	}
	if len(r.Items) != 3 {
		t.Fatalf("items = %d", len(r.Items))
	}
	for _, it := range r.Items {
		if it.Cost == nil || it.Cost.ShareMethod != ShareClusterSplit || it.CostConfidence != ConfidenceLow {
			t.Errorf("%s: %+v (cost conf %s)", it.ID, it.Cost, it.CostConfidence)
		}
		if it.Cost != nil && it.Cost.NodeMonthly != 480 { // 200 + 140 + 140
			t.Errorf("%s: cluster price %v, want 480", it.ID, it.Cost.NodeMonthly)
		}
	}
	for _, n := range r.Nodes {
		if n.Node == "Kubernetes" && (!n.Cluster || n.FullyIdle || len(n.AgentNodes) != 3) {
			t.Errorf("summary = %+v", n)
		}
	}
	checkNodeBudget(t, r, "cluster")
	if !strings.Contains(strings.Join(r.Pricing.Warnings, "\n"), "typo-node") {
		t.Errorf("unmatched node rule not warned: %v", r.Pricing.Warnings)
	}
	if strings.Contains(strings.Join(r.Pricing.Warnings, "\n"), `"node-1"`) {
		t.Errorf("node-1 rule wrongly reported unmatched: %v", r.Pricing.Warnings)
	}
	found := false
	for _, n := range r.Notes {
		found = found || strings.Contains(n, "CLUSTER_NAME")
	}
	if !found {
		t.Errorf("no cluster note: %v", r.Notes)
	}

	// Only some agent nodes priced: the cluster is unpriced, not guessed.
	r2 := build().run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "node-1", Monthly: 200}}}, IdleSpendQuery{})
	for _, it := range r2.Items {
		if it.Cost != nil {
			t.Errorf("partially priced cluster produced a figure for %s: %+v", it.ID, it.Cost)
		}
	}
}

// Reviewer repro: with history disabled, an hourly-called API was flagged
// and priced at $50 and added to the headline.
func TestReviewHistoryOffPricesOnlyOrphans(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.3/hourly-api", "hourly-api", "web_server", "n1").
		liveEdge("10.0.0.3/hourly-api", "10.0.0.2:5432", 5432).
		svc("10.0.0.4:9200", "orphan-es", "database", "n1")
	w.in.historyEnabled = false
	w.in.historyEdges = nil
	w.in.historyServices = nil
	w.in.coverageStart = time.Time{}
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 150, Warmup: -1}, IdleSpendQuery{})
	if findItem(r, "10.0.0.3/hourly-api") != nil {
		t.Errorf("hourly-api flagged with history disabled")
	}
	if ex := findExcluded(r, "10.0.0.3/hourly-api"); ex == nil || ex.Reason != IdleExcludedNoHistory {
		t.Errorf("excluded = %+v", ex)
	}
	it := findItem(r, "10.0.0.4:9200")
	if it == nil || it.InTotal || it.Confidence != ConfidenceLow {
		t.Fatalf("orphan = %+v", it)
	}
	if r.Totals.Monthly != 0 || r.Totals.NotInTotalMonthly <= 0 {
		t.Errorf("totals = %+v", r.Totals)
	}
}

// Reviewer repro: 7 days of silence over a 14-day window was "high", which
// a monthly job reaches.
func TestReviewMonthlyJobIsNotHighConfidence(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.3/invoicer", "invoicer", "web_server", "n1").
		liveEdge("10.0.0.3/invoicer", "10.0.0.2:5432", 5432)
	r := w.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n1", Monthly: 150}}}, IdleSpendQuery{})
	it := findItem(r, "10.0.0.3/invoicer")
	if it == nil || it.IdleConfidence == ConfidenceHigh {
		t.Fatalf("item = %+v", it)
	}
	if !strings.Contains(strings.Join(it.Evidence.Notes, " "), "monthly") {
		t.Errorf("no periodic-job caveat: %v", it.Evidence.Notes)
	}

	// Only 31+ days of observed silence can be high.
	w2 := busyNode(newIdleWorld()).svc("10.0.0.3/invoicer", "invoicer", "web_server", "n1").liveEdge("10.0.0.3/invoicer", "10.0.0.2:5432", 5432)
	w2.in.coverageStart = ago(40 * day)
	for i := range w2.in.historyServices {
		w2.in.historyServices[i].FirstSeen = ago(40 * day)
	}
	r2 := w2.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n1", Monthly: 150}}}, IdleSpendQuery{})
	if it := findItem(r2, "10.0.0.3/invoicer"); it == nil || it.IdleConfidence != ConfidenceHigh {
		t.Errorf("40 observed days should be high: %+v", it)
	}
}

// Reviewer R7: a callee of a just-idle caller is idle only as long as its
// caller has been.
func TestReviewIdleCallersInheritQuiet(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.3/app", "app", "web_server", "n1").
		svc("10.0.0.4/db", "db", "database", "n1").
		liveEdge("10.0.0.3/app", "10.0.0.4/db", 5432).
		pastEdge("10.0.0.1/gw", "10.0.0.3/app", 8080, ago(73*time.Hour))
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 150}, IdleSpendQuery{})
	app, db := findItem(r, "10.0.0.3/app"), findItem(r, "10.0.0.4/db")
	if app == nil || db == nil {
		t.Fatalf("app=%v db=%v", app, db)
	}
	if db.Evidence.QuietSeconds != app.Evidence.QuietSeconds {
		t.Errorf("db quiet %d != app quiet %d", db.Evidence.QuietSeconds, app.Evidence.QuietSeconds)
	}
	if confidenceRank[db.IdleConfidence] > confidenceRank[app.IdleConfidence] {
		t.Errorf("callee more confident than caller")
	}
}

// Documented limitation (reviewer R5): a k8s Service whose name differs from
// its workload's is not matched, so the workload can be flagged if the
// accept side is not captured. It must at least carry the caveat and never
// reach high confidence.
func TestReviewK8sAliasMismatchIsCaveated(t *testing.T) {
	w := newIdleWorld().
		svc("Deploy: shop/web", "web", "web_server", "n1").
		svc("Deploy: shop/api-server", "api-server", "web_server", "n1").
		svc("Svc: shop/api", "api", "web_server", "").
		liveEdge("Deploy: shop/web", "Svc: shop/api", 80).
		liveEdge("Deploy: shop/api-server", "10.0.9.9:5432", 5432).
		node("n1")
	w.svc("203.0.113.9", "external", "unknown", "").liveEdge("203.0.113.9", "Deploy: shop/web", 443)
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 140}, IdleSpendQuery{})
	if it := findItem(r, "Deploy: shop/api-server"); it != nil && it.IdleConfidence == ConfidenceHigh {
		t.Errorf("mismatched alias reached high confidence")
	}
}

func TestUDPServiceHeldBack(t *testing.T) {
	w := busyNode(newIdleWorld()).svc("10.0.0.3/syslog", "syslog", "web_server", "n1")
	w.in.connections = append(w.in.connections, storage.Connection{ID: "u", SourceID: "10.0.0.3/syslog", TargetID: "10.0.0.2:5432", Port: 514, Protocol: "udp", LastSeen: idleNow})
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 100}, IdleSpendQuery{})
	if ex := findExcluded(r, "10.0.0.3/syslog"); ex == nil || ex.Reason != IdleExcludedUDP {
		t.Errorf("excluded = %+v", ex)
	}
}

// Reviewer repro: process names went into the ticket raw - a name like
// ![t](http://e.vil/p.png) rendered as a tracking pixel; CSV missed
// whitespace-prefixed formulas.
func TestReviewRenderEscaping(t *testing.T) {
	w := newIdleWorld().
		svc(" =1+1/x", " =1+1", "web_server", "[n](http://e.vil)").
		svc("10.0.0.9/srv", "srv", "web_server", "n1").
		svc("10.0.0.8/a", "![t](http://e.vil/p.png)", "web_server", "n1").
		svc("10.0.0.7/b", "<img src=x onerror=alert(1)>", "web_server", "n1").
		liveEdge("10.0.0.9/srv", "10.0.0.8/a", 80).
		node("n1")
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 100, Currency: "<b>X</b>|"}, IdleSpendQuery{})
	if r.Currency != "USD" || len(r.Pricing.Warnings) == 0 {
		t.Errorf("bad currency accepted: %q %v", r.Currency, r.Pricing.Warnings)
	}
	mdOut := RenderIdleSpendMarkdown(r)
	for _, bad := range []string{"](http", "![", "<img", "<b>", "[n]"} {
		if regexp.MustCompile(`(^|[^\\])` + regexp.QuoteMeta(bad)).MatchString(mdOut) {
			t.Errorf("markdown contains raw %q:\n%s", bad, mdOut)
		}
	}
	csvOut, _ := RenderIdleSpendCSV(r)
	if !strings.Contains(string(csvOut), "' =1+1") {
		t.Errorf("whitespace-prefixed formula not neutralised:\n%s", csvOut)
	}
	for _, c := range []struct{ in, want string }{
		{"=x", "'=x"}, {" =x", "' =x"}, {"\t@x", "'\t@x"}, {"\x01+x", "'\x01+x"}, {"plain", "plain"}, {"a=b", "a=b"},
	} {
		if got := csvSafe(c.in); got != c.want {
			t.Errorf("csvSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- through the real store -------------------------------------------------

// Concurrent requests share one computation, and repeat requests within the
// TTL are served from cache.
func TestIdleSpendCacheCoalesces(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 100})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ts.GetIdleSpend(context.Background(), IdleSpendQuery{Limit: 1 + i%3}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := ts.idle.computations.Load(); n != 1 {
		t.Errorf("computations = %d, want 1", n)
	}
	_, _ = ts.GetIdleSpend(context.Background(), IdleSpendQuery{AssumeNodeMonthly: 5})
	if n := ts.idle.computations.Load(); n != 2 {
		t.Errorf("a different what-if should compute separately: %d", n)
	}
	for i := 0; i < 100; i++ { // caller-controlled keys stay bounded
		_, _ = ts.GetIdleSpend(context.Background(), IdleSpendQuery{AssumeNodeMonthly: float64(10 + i)})
	}
	ts.idle.cacheMu.Lock()
	size := len(ts.idle.cache)
	ts.idle.cacheMu.Unlock()
	if size > maxIdleCacheEntries {
		t.Errorf("cache grew to %d", size)
	}
}

// Throughput keeps both endpoints of a long-lived connection alive in
// current state (the root cause of the phantom-endpoint repro).
func TestThroughputKeepsEndpointsAlive(t *testing.T) {
	ts, store := newHistoryTestService(t, false)
	ctx := context.Background()
	for _, id := range []string{"10.0.0.5/worker", "10.0.0.6:5432"} {
		if err := ts.AddOrUpdateService(ctx, &storage.Service{ID: id, Name: id, Node: "n1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ts.AddConnection(ctx, &storage.Connection{ID: "pool", SourceID: "10.0.0.5/worker", TargetID: "10.0.0.6:5432", Port: 5432, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Services().Get(ctx, "10.0.0.6:5432")
	time.Sleep(20 * time.Millisecond)
	if err := ts.UpdateConnectionStats(ctx, "pool", 1, 1, 1, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"10.0.0.5/worker", "10.0.0.6:5432"} {
		after, _ := store.Services().Get(ctx, id)
		if after == nil || !after.LastSeen.After(before.LastSeen) {
			t.Errorf("%s last_seen not refreshed by throughput", id)
		}
	}
	// A pruned endpoint is not resurrected.
	_ = store.Services().Delete(ctx, "10.0.0.5/worker")
	ts.idle.liveSeen = nil
	_ = ts.UpdateConnectionStats(ctx, "pool", 2, 2, 1, 1, 2, 2)
	if s, _ := store.Services().Get(ctx, "10.0.0.5/worker"); s != nil {
		t.Errorf("pruned service resurrected")
	}
}
