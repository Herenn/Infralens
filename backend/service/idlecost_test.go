package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

var idleNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return idleNow.Add(-d) }

const day = 24 * time.Hour

// idleWorld builds estimator inputs. History is enabled and covers 14 days
// unless a test changes it.
type idleWorld struct {
	in    idleInputs
	ports map[string][]int
}

func newIdleWorld() *idleWorld {
	return &idleWorld{in: idleInputs{
		now:            idleNow,
		metrics:        map[string]storage.NodeMetrics{},
		historyEnabled: true,
		coverageStart:  ago(14 * day),
		window:         14 * day,
		maxGap:         5 * time.Minute,
	}, ports: map[string][]int{}}
}

func (w *idleWorld) svc(id, name, typ, node string) *idleWorld {
	w.in.services = append(w.in.services, storage.Service{
		ID: id, Name: name, Type: typ, Node: node, LastSeen: idleNow, CreatedAt: ago(30 * day),
	})
	w.in.historyServices = append(w.in.historyServices, storage.ServiceInterval{
		ServiceID: id, Name: name, Type: typ, Node: node, FirstSeen: ago(14 * day), LastSeen: idleNow,
	})
	return w
}

func (w *idleWorld) liveEdge(src, dst string, port uint16) *idleWorld {
	w.in.connections = append(w.in.connections, storage.Connection{
		ID: src + "->" + dst, SourceID: src, TargetID: dst, Port: port, LastSeen: idleNow, BytesSent: 1000,
	})
	w.in.historyEdges = append(w.in.historyEdges, storage.ConnectionInterval{
		ConnectionID: src + "->" + dst, SourceID: src, TargetID: dst, Port: port, FirstSeen: ago(14 * day), LastSeen: idleNow,
	})
	return w
}

func (w *idleWorld) pastEdge(src, dst string, port uint16, ended time.Time) *idleWorld {
	w.in.historyEdges = append(w.in.historyEdges, storage.ConnectionInterval{
		ConnectionID: src + "->" + dst, SourceID: src, TargetID: dst, Port: port, FirstSeen: ago(14 * day), LastSeen: ended,
	})
	return w
}

func (w *idleWorld) node(name string) *idleWorld {
	w.in.metrics[name] = storage.NodeMetrics{NodeName: name, CPUPercent: 20, MemPercent: 40, LastSeen: idleNow}
	return w
}

func (w *idleWorld) run(cfg IdleCostConfig, q IdleSpendQuery) *IdleSpendReport {
	in := w.in
	in.listenPorts = func(id string) []int { return w.ports[id] }
	return estimateIdleSpend(in, cfg.withDefaults(), q)
}

func findItem(r *IdleSpendReport, id string) *IdleServiceItem {
	for i := range r.Items {
		if r.Items[i].ID == id {
			return &r.Items[i]
		}
	}
	return nil
}

func findExcluded(r *IdleSpendReport, id string) *IdleExclusion {
	for i := range r.Excluded {
		if r.Excluded[i].ID == id {
			return &r.Excluded[i]
		}
	}
	return nil
}

// busyNode is a node with a gateway receiving external traffic and calling a
// database - the baseline nothing should flag.
func busyNode(w *idleWorld) *idleWorld {
	return w.
		svc("1.2.3.4", "external", "unknown", "").
		svc("10.0.0.1/gw", "gateway", "web_server", "n1").
		svc("10.0.0.2:5432", "postgres", "database", "n1").
		liveEdge("1.2.3.4", "10.0.0.1/gw", 443).
		liveEdge("10.0.0.1/gw", "10.0.0.2:5432", 5432).
		node("n1")
}

func TestIdleSpendClassification(t *testing.T) {
	type want struct {
		id       string
		category string // "" = must not be an item
		excluded string // "" = must not be excluded
		idleConf string
	}
	tests := []struct {
		name  string
		build func(w *idleWorld)
		want  []want
	}{
		{
			name:  "busy graph flags nothing",
			build: func(w *idleWorld) { busyNode(w) },
			want: []want{
				{id: "10.0.0.1/gw"}, {id: "10.0.0.2:5432"}, {id: "1.2.3.4"},
			},
		},
		{
			name: "server with outbound only and no inbound in the whole window",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.3/reports", "reports", "web_server", "n1").liveEdge("10.0.0.3/reports", "10.0.0.2:5432", 5432)
			},
			// 14 days observed: medium at best (high needs >31 days).
			want: []want{{id: "10.0.0.3/reports", category: IdleCategoryNoInbound, idleConf: ConfidenceMedium}},
		},
		{
			name: "inbound stopped 4 days ago is quiet, medium confidence",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.3/admin", "admin", "web_server", "n1").
					liveEdge("10.0.0.3/admin", "10.0.0.2:5432", 5432).
					pastEdge("10.0.0.1/gw", "10.0.0.3/admin", 8080, ago(4*day))
			},
			want: []want{{id: "10.0.0.3/admin", category: IdleCategoryQuiet, idleConf: ConfidenceMedium}},
		},
		{
			name: "inbound stopped 9 days ago is quiet, still only medium",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.3/admin", "admin", "web_server", "n1").
					liveEdge("10.0.0.3/admin", "10.0.0.2:5432", 5432).
					pastEdge("10.0.0.1/gw", "10.0.0.3/admin", 8080, ago(9*day))
			},
			want: []want{{id: "10.0.0.3/admin", category: IdleCategoryQuiet, idleConf: ConfidenceMedium}},
		},
		{
			name: "inbound 1 day ago is still in use",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.3/admin", "admin", "web_server", "n1").
					liveEdge("10.0.0.3/admin", "10.0.0.2:5432", 5432).
					pastEdge("10.0.0.1/gw", "10.0.0.3/admin", 8080, ago(1*day))
			},
			want: []want{{id: "10.0.0.3/admin"}},
		},
		{
			name: "no connections at all is an orphan",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.9:9200", "search-old", "database", "n1")
			},
			want: []want{{id: "10.0.0.9:9200", category: IdleCategoryOrphan, idleConf: ConfidenceMedium}},
		},
		{
			name: "outbound-only application is a worker, not idle",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.4/etl", "nightly-etl", "application", "n1").liveEdge("10.0.0.4/etl", "10.0.0.2:5432", 5432)
			},
			want: []want{{id: "10.0.0.4/etl", excluded: IdleExcludedOutboundOnly}},
		},
		{
			name: "cron job between runs (no live edges, outbound in history) is not an orphan",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.4/cron", "cron", "application", "n1").pastEdge("10.0.0.4/cron", "10.0.0.2:5432", 5432, ago(2*time.Hour))
			},
			want: []want{{id: "10.0.0.4/cron", excluded: IdleExcludedOutboundOnly}},
		},
		{
			// A metrics/health port is normal for a worker; listening is not
			// evidence of being a server nobody calls.
			name: "application with listening ports and outbound traffic is still a worker",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.4/app", "app", "application", "n1").liveEdge("10.0.0.4/app", "10.0.0.2:5432", 5432)
				w.ports["10.0.0.4/app"] = []int{8080}
			},
			want: []want{{id: "10.0.0.4/app", excluded: IdleExcludedOutboundOnly}},
		},
		{
			name: "monitoring scraper is excluded",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.5:9090", "prometheus", "monitoring", "n1").liveEdge("10.0.0.5:9090", "10.0.0.1/gw", 8080)
			},
			want: []want{{id: "10.0.0.5:9090", excluded: IdleExcludedMonitoring}},
		},
		{
			name: "monitoring scrapes do not count as use",
			build: func(w *idleWorld) {
				busyNode(w).
					svc("10.0.0.5:9090", "prometheus", "monitoring", "n1").
					svc("10.0.0.6/api", "old-api", "web_server", "n1").
					liveEdge("10.0.0.5:9090", "10.0.0.6/api", 8080).
					liveEdge("10.0.0.6/api", "10.0.0.2:5432", 5432)
			},
			want: []want{{id: "10.0.0.6/api", category: IdleCategoryNoInbound, idleConf: ConfidenceMedium}},
		},
		{
			name: "database only reached through an idle service is idle too",
			build: func(w *idleWorld) {
				busyNode(w).
					svc("10.0.0.7/legacy", "legacy", "web_server", "n2").
					svc("10.0.0.8:3306", "mysql", "database", "n2").
					liveEdge("10.0.0.7/legacy", "10.0.0.8:3306", 3306).
					node("n2")
			},
			want: []want{
				{id: "10.0.0.7/legacy", category: IdleCategoryNoInbound},
				{id: "10.0.0.8:3306", category: IdleCategoryIdleCallersOnly, idleConf: ConfidenceMedium},
			},
		},
		{
			name: "database reached by address from another node is not flagged",
			build: func(w *idleWorld) {
				// The agent on the DB host recorded the process; the client's
				// agent recorded only the address. Same thing, two IDs.
				busyNode(w).
					svc("10.0.0.20/postgres", "postgres", "database", "n3").
					svc("10.0.0.20:5432", ":5432", "database", "").
					liveEdge("10.0.0.1/gw", "10.0.0.20:5432", 5432).
					liveEdge("10.0.0.20/postgres", "10.0.0.2:5432", 5432)
			},
			want: []want{{id: "10.0.0.20/postgres", excluded: IdleExcludedReachedByAddr}, {id: "10.0.0.20:5432"}},
		},
		{
			name: "workload reached only through its Kubernetes Service is not flagged",
			build: func(w *idleWorld) {
				busyNode(w).
					svc("Deploy: shop/api", "api", "web_server", "n1").
					svc("Svc: shop/api", "api", "web_server", "").
					liveEdge("10.0.0.1/gw", "Svc: shop/api", 80).
					liveEdge("Deploy: shop/api", "10.0.0.2:5432", 5432)
			},
			want: []want{{id: "Deploy: shop/api", excluded: IdleExcludedReachedByAddr}},
		},
		{
			name: "monitoring tool reading a database counts as use",
			build: func(w *idleWorld) {
				busyNode(w).
					svc("10.0.0.5/grafana", "grafana", "monitoring", "n1").
					svc("10.0.0.6:5432", "grafana-db", "database", "n1").
					liveEdge("10.0.0.5/grafana", "10.0.0.6:5432", 5432)
			},
			want: []want{{id: "10.0.0.6:5432"}},
		},
		{
			name: "InfraLens components are never flagged",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.30/infralens-agent", "infralens-agent", "web_server", "n1")
			},
			want: []want{{id: "10.0.0.30/infralens-agent", excluded: IdleExcludedInfraLens}},
		},
		{
			name: "external endpoints are not ours to price",
			build: func(w *idleWorld) {
				busyNode(w).svc("8.8.8.8", "external", "unknown", "")
			},
			want: []want{{id: "8.8.8.8"}},
		},
		{
			name: "new service inside warmup is held back",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.40/new", "new", "web_server", "n1")
				w.in.historyServices[len(w.in.historyServices)-1].FirstSeen = ago(2 * time.Hour)
				w.in.services[len(w.in.services)-1].CreatedAt = ago(2 * time.Hour)
			},
			want: []want{{id: "10.0.0.40/new", excluded: IdleExcludedWarmup}},
		},
		{
			name: "not quiet long enough yet",
			build: func(w *idleWorld) {
				busyNode(w).svc("10.0.0.41/api", "api", "web_server", "n1").liveEdge("10.0.0.41/api", "10.0.0.2:5432", 5432)
				w.in.coverageStart = ago(2 * day)
			},
			want: []want{{id: "10.0.0.41/api", excluded: IdleExcludedNotQuietEnough}},
		},
		{
			name: "decommission candidate from history",
			build: func(w *idleWorld) {
				busyNode(w)
				w.in.stale = []storage.ServiceInterval{{ServiceID: "10.0.0.50/exporter", Name: "exporter", Type: "application", Node: "n1", FirstSeen: ago(14 * day), LastSeen: ago(10 * day)}}
			},
			want: []want{{id: "10.0.0.50/exporter", category: IdleCategoryStale, idleConf: ConfidenceMedium}},
		},
		{
			name: "decommission candidate on a node that is gone",
			build: func(w *idleWorld) {
				busyNode(w)
				w.in.stale = []storage.ServiceInterval{{ServiceID: "10.9.0.1/x", Name: "x", Node: "gone-node", LastSeen: ago(10 * day)}}
			},
			want: []want{{id: "10.9.0.1/x", excluded: IdleExcludedNodeGone}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newIdleWorld()
			tt.build(w)
			r := w.run(IdleCostConfig{DefaultNodeMonthly: 100, Warmup: -1}, IdleSpendQuery{})
			for _, wt := range tt.want {
				it := findItem(r, wt.id)
				ex := findExcluded(r, wt.id)
				if wt.category == "" && it != nil {
					t.Errorf("%s: unexpectedly flagged as %s (%v)", wt.id, it.Category, it.Evidence.Notes)
				}
				if wt.category != "" {
					if it == nil {
						t.Errorf("%s: expected category %s, not flagged (excluded: %+v)", wt.id, wt.category, ex)
						continue
					}
					if it.Category != wt.category {
						t.Errorf("%s: category = %s, want %s", wt.id, it.Category, wt.category)
					}
					if wt.idleConf != "" && it.IdleConfidence != wt.idleConf {
						t.Errorf("%s: idle confidence = %s, want %s", wt.id, it.IdleConfidence, wt.idleConf)
					}
					if len(it.Evidence.Notes) == 0 {
						t.Errorf("%s: no evidence notes", wt.id)
					}
				}
				if wt.excluded == "" && ex != nil && wt.category == "" {
					t.Errorf("%s: unexpectedly excluded (%s)", wt.id, ex.Reason)
				}
				if wt.excluded != "" && (ex == nil || ex.Reason != wt.excluded) {
					t.Errorf("%s: excluded = %+v, want reason %s", wt.id, ex, wt.excluded)
				}
			}
		})
	}
}

func TestIdleSpendCostAttribution(t *testing.T) {
	// n1 has 3 services (gateway, postgres, reports); reports is idle.
	base := func() *idleWorld {
		return busyNode(newIdleWorld()).
			svc("10.0.0.3/reports", "reports", "web_server", "n1").
			liveEdge("10.0.0.3/reports", "10.0.0.2:5432", 5432)
	}

	tests := []struct {
		name        string
		world       func() *idleWorld
		cfg         IdleCostConfig
		q           IdleSpendQuery
		id          string
		wantMonthly float64 // <0 = expect nil cost
		wantSource  string
		wantMethod  string
		wantConf    string
	}{
		{
			name: "unconfigured prices give no number", world: base, id: "10.0.0.3/reports",
			wantMonthly: -1, wantConf: ConfidenceNone,
		},
		{
			name: "assumed price is used and rated low", world: base, q: IdleSpendQuery{AssumeNodeMonthly: 90},
			id: "10.0.0.3/reports", wantMonthly: 30, wantSource: PriceSourceAssumed, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
		{
			name: "default node price split evenly", world: base, cfg: IdleCostConfig{DefaultNodeMonthly: 150},
			id: "10.0.0.3/reports", wantMonthly: 50, wantSource: PriceSourceDefault, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
		{
			name: "node rule beats default", world: base,
			cfg: IdleCostConfig{DefaultNodeMonthly: 150, NodePrices: []PriceRule{{Pattern: "n*", Monthly: 300}}},
			id:  "10.0.0.3/reports", wantMonthly: 100, wantSource: PriceSourceNodeRule, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
		{
			name: "configured default ignores the what-if", world: base,
			cfg: IdleCostConfig{DefaultNodeMonthly: 150}, q: IdleSpendQuery{AssumeNodeMonthly: 999},
			id: "10.0.0.3/reports", wantMonthly: 50, wantSource: PriceSourceDefault, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
		{
			name: "service rule on a priced node is carved out of the node price",
			world: func() *idleWorld {
				w := base()
				return w
			},
			cfg: IdleCostConfig{DefaultNodeMonthly: 150, ServicePrices: []PriceRule{{Pattern: "reports", Monthly: 42.5}}},
			id:  "10.0.0.3/reports", wantMonthly: 42.5, wantSource: PriceSourceServiceRule, wantMethod: ShareCarveOut, wantConf: ConfidenceHigh,
		},
		{
			name: "only service on its node takes the whole node (default -> medium)",
			world: func() *idleWorld {
				return busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9").node("n9")
			},
			cfg: IdleCostConfig{DefaultNodeMonthly: 80},
			id:  "10.0.1.1/solo", wantMonthly: 80, wantSource: PriceSourceDefault, wantMethod: ShareWholeNode, wantConf: ConfidenceMedium,
		},
		{
			name: "only service on its node with a node rule is high",
			world: func() *idleWorld {
				return busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9").node("n9")
			},
			cfg: IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n9", Monthly: 80}}},
			id:  "10.0.1.1/solo", wantMonthly: 80, wantSource: PriceSourceNodeRule, wantMethod: ShareWholeNode, wantConf: ConfidenceHigh,
		},
		{
			name: "fully idle node is attributed whole",
			world: func() *idleWorld {
				return busyNode(newIdleWorld()).
					svc("10.0.0.7/legacy", "legacy", "web_server", "n2").
					svc("10.0.0.8:3306", "mysql", "database", "n2").
					liveEdge("10.0.0.7/legacy", "10.0.0.8:3306", 3306)
			},
			cfg: IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n2", Monthly: 200}}},
			id:  "10.0.0.7/legacy", wantMonthly: 100, wantSource: PriceSourceNodeRule, wantMethod: ShareWholeNode, wantConf: ConfidenceHigh,
		},
		{
			name: "missing host metrics still prices a live service",
			world: func() *idleWorld {
				w := base()
				w.in.metrics = map[string]storage.NodeMetrics{}
				return w
			},
			cfg: IdleCostConfig{DefaultNodeMonthly: 150},
			id:  "10.0.0.3/reports", wantMonthly: 50, wantSource: PriceSourceDefault, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
		{
			name: "stale service on a node with zero live services takes the whole node, low",
			world: func() *idleWorld {
				w := busyNode(newIdleWorld()).node("empty-node")
				w.in.stale = []storage.ServiceInterval{{ServiceID: "10.0.5.1/old", Name: "old", Node: "empty-node", LastSeen: ago(10 * day)}}
				return w
			},
			cfg: IdleCostConfig{DefaultNodeMonthly: 60},
			id:  "10.0.5.1/old", wantMonthly: 60, wantSource: PriceSourceDefault, wantMethod: ShareWholeNode, wantConf: ConfidenceLow,
		},
		{
			name: "stale service counts itself in a busy node's split",
			world: func() *idleWorld {
				w := busyNode(newIdleWorld())
				w.in.stale = []storage.ServiceInterval{{ServiceID: "10.0.5.1/old", Name: "old", Node: "n1", LastSeen: ago(10 * day)}}
				return w
			},
			cfg: IdleCostConfig{DefaultNodeMonthly: 90},
			id:  "10.0.5.1/old", wantMonthly: 30, wantSource: PriceSourceDefault, wantMethod: ShareEvenSplit, wantConf: ConfidenceLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.world().run(tt.cfg, tt.q)
			it := findItem(r, tt.id)
			if it == nil {
				t.Fatalf("%s not flagged; items=%+v excluded=%+v", tt.id, r.Items, r.Excluded)
			}
			if tt.wantMonthly < 0 {
				if it.Cost != nil {
					t.Fatalf("expected no cost, got %+v", it.Cost)
				}
				if it.CostConfidence != ConfidenceNone {
					t.Errorf("cost confidence = %s, want none", it.CostConfidence)
				}
				if it.Confidence != it.IdleConfidence {
					t.Errorf("unpriced confidence should follow idle confidence")
				}
				return
			}
			if it.Cost == nil {
				t.Fatalf("expected a cost")
			}
			if it.Cost.Monthly != tt.wantMonthly {
				t.Errorf("monthly = %v, want %v (%s)", it.Cost.Monthly, tt.wantMonthly, it.Cost.Explanation)
			}
			if it.Cost.Annual != round2(tt.wantMonthly*12) {
				t.Errorf("annual = %v, want %v", it.Cost.Annual, tt.wantMonthly*12)
			}
			if it.Cost.PriceSource != tt.wantSource || it.Cost.ShareMethod != tt.wantMethod {
				t.Errorf("source/method = %s/%s, want %s/%s", it.Cost.PriceSource, it.Cost.ShareMethod, tt.wantSource, tt.wantMethod)
			}
			if it.CostConfidence != tt.wantConf {
				t.Errorf("cost confidence = %s, want %s", it.CostConfidence, tt.wantConf)
			}
			if it.Cost.Explanation == "" {
				t.Error("no explanation")
			}
		})
	}
}

func TestIdleSpendTotalsAndRanking(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.0.3/reports", "reports", "web_server", "n1").
		liveEdge("10.0.0.3/reports", "10.0.0.2:5432", 5432).
		svc("10.0.1.1/solo", "solo", "web_server", "n9").node("n9")

	r := w.run(IdleCostConfig{DefaultNodeMonthly: 120, NodePrices: []PriceRule{{Pattern: "n9", Monthly: 300}}}, IdleSpendQuery{})
	if r.Totals.Services != 2 || r.Totals.Priced != 2 {
		t.Fatalf("totals = %+v", r.Totals)
	}
	if r.Totals.Monthly != 340 || r.Totals.Annual != 4080 {
		t.Errorf("monthly/annual = %v/%v, want 340/4080", r.Totals.Monthly, r.Totals.Annual)
	}
	if r.Totals.MonthlyByConfidence[ConfidenceMedium] != 300 || r.Totals.MonthlyByConfidence[ConfidenceLow] != 40 {
		t.Errorf("by confidence = %v", r.Totals.MonthlyByConfidence)
	}
	if r.Items[0].ID != "10.0.1.1/solo" {
		t.Errorf("highest expected saving should rank first, got %s", r.Items[0].ID)
	}

	limited := w.run(IdleCostConfig{DefaultNodeMonthly: 120}, IdleSpendQuery{Limit: 1})
	if len(limited.Items) != 1 || limited.Totals.Services != 2 {
		t.Errorf("limit should cap items but not totals: items=%d totals=%+v", len(limited.Items), limited.Totals)
	}

	var n9 *IdleNodeSummary
	for i := range r.Nodes {
		if r.Nodes[i].Node == "n9" {
			n9 = &r.Nodes[i]
		}
	}
	if n9 == nil || !n9.FullyIdle || n9.Monthly == nil || *n9.Monthly != 300 {
		t.Errorf("n9 summary = %+v", n9)
	}
	foundNote := false
	for _, n := range r.Notes {
		if strings.Contains(n, "n9") && strings.Contains(n, "whole node") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("expected a fully-idle-node note, got %v", r.Notes)
	}
}

func TestIdleSpendUnconfiguredIsExplanatory(t *testing.T) {
	w := busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9")
	r := w.run(IdleCostConfig{}, IdleSpendQuery{})
	if r.Pricing.Configured || r.Pricing.Source != "none" {
		t.Errorf("pricing = %+v", r.Pricing)
	}
	if r.Totals.Monthly != 0 || r.Totals.Unpriced != 1 {
		t.Errorf("totals = %+v", r.Totals)
	}
	if len(r.Notes) == 0 || !strings.Contains(r.Notes[0], "IDLE_COST_NODE_MONTHLY") {
		t.Errorf("expected a configure-me note, got %v", r.Notes)
	}

	assumed := w.run(IdleCostConfig{}, IdleSpendQuery{AssumeNodeMonthly: 50})
	if assumed.Pricing.Source != "assumed" || assumed.Pricing.AssumedNodeMonthly == nil {
		t.Errorf("assumed pricing = %+v", assumed.Pricing)
	}
}

func TestIdleSpendWarmupAndNoHistory(t *testing.T) {
	t.Run("history younger than warmup flags nothing", func(t *testing.T) {
		w := busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9")
		w.in.coverageStart = ago(20 * time.Minute)
		r := w.run(IdleCostConfig{DefaultNodeMonthly: 100, Warmup: -1}, IdleSpendQuery{})
		if !r.Evidence.WarmingUp || len(r.Items) != 0 {
			t.Errorf("warming_up=%v items=%d", r.Evidence.WarmingUp, len(r.Items))
		}
	})
	t.Run("warmup 0 disables it", func(t *testing.T) {
		w := busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9")
		w.in.coverageStart = ago(20 * time.Minute)
		r := w.run(IdleCostConfig{DefaultNodeMonthly: 100, Warmup: 0, QuietAfter: 10 * time.Minute}, IdleSpendQuery{})
		if r.Evidence.WarmingUp || findItem(r, "10.0.1.1/solo") == nil {
			t.Errorf("expected solo flagged with warmup disabled; excluded=%+v", r.Excluded)
		}
		if it := findItem(r, "10.0.1.1/solo"); it != nil && it.IdleConfidence != ConfidenceMedium {
			t.Errorf("20m quiet with 10m threshold should be medium, got %s", it.IdleConfidence)
		}
	})
	t.Run("history disabled caps confidence at low", func(t *testing.T) {
		w := busyNode(newIdleWorld()).svc("10.0.1.1/solo", "solo", "web_server", "n9")
		w.in.historyEnabled = false
		w.in.historyEdges = nil
		w.in.historyServices = nil
		w.in.coverageStart = time.Time{}
		r := w.run(IdleCostConfig{NodePrices: []PriceRule{{Pattern: "n9", Monthly: 100}}}, IdleSpendQuery{})
		it := findItem(r, "10.0.1.1/solo")
		if it == nil {
			t.Fatalf("expected solo flagged; excluded=%+v", r.Excluded)
		}
		if it.IdleConfidence != ConfidenceLow || it.Confidence != ConfidenceLow {
			t.Errorf("confidence = %s/%s, want low", it.IdleConfidence, it.Confidence)
		}
		if r.Evidence.HistoryEnabled || len(r.Notes) == 0 {
			t.Errorf("expected history-disabled note, got %v", r.Notes)
		}
	})
}

func TestIdleSpendUserExclude(t *testing.T) {
	w := busyNode(newIdleWorld()).svc("10.0.1.1/dr-standby", "dr-standby", "database", "n9")
	r := w.run(IdleCostConfig{Exclude: []string{"dr-*"}}, IdleSpendQuery{})
	if ex := findExcluded(r, "10.0.1.1/dr-standby"); ex == nil || ex.Reason != IdleExcludedUser {
		t.Errorf("excluded = %+v", ex)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "anything/at/all", true},
		{"10.0.1.*", "10.0.1.10/nginx", true},
		{"gpu-*", "GPU-node-1", true},
		{"db-?", "db-1", true},
		{"db-?", "db-10", false},
		{"*/postgres", "10.0.3.10/postgres", true},
		{"nginx", "10.0.1.10/nginx", false},
		{"", "", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
	}
	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.s); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestIdleSpendRenderers(t *testing.T) {
	w := busyNode(newIdleWorld()).
		svc("10.0.1.1/=cmd|evil", "=cmd|evil", "web_server", "n9").node("n9")
	r := w.run(IdleCostConfig{DefaultNodeMonthly: 1240}, IdleSpendQuery{})

	md := RenderIdleSpendMarkdown(r)
	for _, want := range []string{"~$1,240/month", "| 1 |", `\|evil`, "candidates to verify with the owning team"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}

	csvOut, err := RenderIdleSpendCSV(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(csvOut), "'=cmd|evil") {
		t.Errorf("CSV did not neutralise a formula-looking name:\n%s", csvOut)
	}
	if !strings.HasPrefix(string(csvOut), "id,name,node,category") {
		t.Errorf("CSV header wrong:\n%s", csvOut)
	}
}

// TestGetIdleSpendEndToEnd drives the real store: history backfill, current
// state, and the estimator together.
func TestGetIdleSpendEndToEnd(t *testing.T) {
	ts, _ := newHistoryTestService(t, true)
	ctx := context.Background()
	now := time.Now()

	gw := &storage.Service{ID: "10.0.0.1/gw", Name: "gateway", Type: "web_server", Node: "n1"}
	ext := &storage.Service{ID: "1.2.3.4", Name: "external", Type: "unknown", Tech: "External"}
	old := &storage.Service{ID: "10.0.0.3/reports", Name: "reports", Type: "web_server", Node: "n1"}
	db := &storage.Service{ID: "10.0.0.2:5432", Name: "postgres", Type: "database", Node: "n1"}
	for _, s := range []*storage.Service{gw, ext, old, db} {
		if err := ts.BackfillServiceHistory(ctx, s, now.Add(-14*day), now); err != nil {
			t.Fatal(err)
		}
	}
	oldInbound := &storage.Connection{ID: "10.0.0.1/gw->10.0.0.3/reports:8080", SourceID: gw.ID, TargetID: old.ID, Port: 8080}
	if err := ts.BackfillConnectionHistory(ctx, oldInbound, now.Add(-14*day), now.Add(-9*day)); err != nil {
		t.Fatal(err)
	}

	for _, s := range []*storage.Service{gw, ext, old, db} {
		cp := *s
		if err := ts.AddOrUpdateService(ctx, &cp); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []storage.Connection{
		{ID: "1.2.3.4->10.0.0.1/gw:443", SourceID: ext.ID, TargetID: gw.ID, Port: 443},
		{ID: "10.0.0.1/gw->10.0.0.2:5432:5432", SourceID: gw.ID, TargetID: db.ID, Port: 5432},
		{ID: "10.0.0.3/reports->10.0.0.2:5432:5432", SourceID: old.ID, TargetID: db.ID, Port: 5432},
	} {
		c := c
		c.LastSeen = now
		if err := ts.AddConnection(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}

	ts.ConfigureIdleCost(IdleCostConfig{DefaultNodeMonthly: 300})
	r, err := ts.GetIdleSpend(ctx, IdleSpendQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 1 || r.Items[0].ID != old.ID {
		t.Fatalf("items = %+v, excluded = %+v, notes = %v", r.Items, r.Excluded, r.Notes)
	}
	it := r.Items[0]
	if it.Category != IdleCategoryQuiet || it.IdleConfidence != ConfidenceMedium {
		t.Errorf("category/conf = %s/%s", it.Category, it.IdleConfidence)
	}
	if it.Evidence.LastInbound == nil || now.Sub(*it.Evidence.LastInbound) < 8*day {
		t.Errorf("last inbound = %v", it.Evidence.LastInbound)
	}
	if it.Cost == nil || it.Cost.Monthly != 100 {
		t.Errorf("cost = %+v", it.Cost)
	}
	if r.Evidence.ObservedSeconds < int64((13 * day).Seconds()) {
		t.Errorf("observed = %ds, backfill should cover ~14d", r.Evidence.ObservedSeconds)
	}
}
