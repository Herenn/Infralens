package demo

import (
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// The idle-spend part of the demo cast. Idle detection is only meaningful
// against a past ("nothing has called this in 9 days"); that past is part of
// the demo's one backfilled history (story.go), where these services' callers
// stop at believable points. Everything here is simulated, like the rest of
// demo mode. None of these services takes part in the live drift script, so
// the Idle-spend panel and the Changes feed never tell contradicting stories
// about the same service.

const nodeLegacy = "demo-legacy-1"

// The idle candidates and the guard example, by ID.
const (
	idReportsV1     = "10.0.4.10/reports-v1"
	idReportsMySQL  = "10.0.4.11:3306"
	idAdminPanel    = "10.0.2.17/admin-panel"
	idNightlyETL    = "10.0.2.15/nightly-etl"
	idSearchV1      = "10.0.3.17:9200"
	idBatchExporter = "10.0.2.16/batch-exporter" // history only
)

// idleDemoServices are the live services the idle-spend panel finds, plus
// one that it deliberately does not (nightly-etl, an outbound-only worker).
func idleDemoServices(svc func(id, name, svcType, tech, icon, node, ip string) storage.Service) []storage.Service {
	return []storage.Service{
		// A whole legacy node nothing uses any more: reports-v1 lost its last
		// caller 9 days ago but still holds its MySQL pool open.
		svc(idReportsV1, "reports-v1", "web_server", "Java/Tomcat", "http", nodeLegacy, "10.0.4.10"),
		svc(idReportsMySQL, "reports-mysql", "database", "MySQL 5.7", "mysql", nodeLegacy, "10.0.4.11"),

		// Last called by the gateway 4 days ago.
		svc(idAdminPanel, "admin-panel", "web_server", "Node.js/Express", "nodejs", "demo-app-1", "10.0.2.17"),

		// Talks outbound only, never called: a worker, not idle. Shows up
		// under "not counted".
		svc(idNightlyETL, "nightly-etl", "application", "Python", "python", "demo-app-1", "10.0.2.15"),

		// The old Elasticsearch that "search" (ES 8.13, adopted 5 hours ago -
		// story.go) replaces: no connections at all for two weeks.
		svc(idSearchV1, "search-v1", "database", "Elasticsearch 6.8", "elasticsearch", "demo-data-1", "10.0.3.17"),
	}
}

func idleDemoConnections(conn func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection) []*demoConnection {
	return []*demoConnection{
		conn(idReportsV1, idReportsMySQL, 3306, 2, 6),
		// Prometheus still scrapes it - which is not anyone using it.
		conn("10.0.3.13:9090", idReportsV1, 8080, 1, 12),
		conn(idAdminPanel, "10.0.3.11:6379", 6379, 0.2, 0.3),
		conn(idNightlyETL, "10.0.3.10:5432", 5432, 3, 25),
	}
}

// configureIdleCost gives the demo illustrative prices so the idle-spend
// panel shows numbers out of the box - unless the operator configured real
// ones, which always win. Labelled "demo" in every report.
func (s *Simulator) configureIdleCost() {
	current := s.topology.IdleCostConfig()
	if current.Priced() {
		return
	}
	cfg := current
	cfg.Source = "demo"
	cfg.DefaultNodeMonthly = 140
	cfg.NodePrices = []service.PriceRule{
		{Pattern: "demo-data-*", Monthly: 420},
		{Pattern: "demo-legacy-*", Monthly: 190},
	}
	if !s.topology.HistoryEnabled() {
		// Without history there is no past to clear warmup with; show the
		// (low-confidence) current-window verdicts rather than nothing.
		cfg.Warmup = 0
	}
	s.topology.ConfigureIdleCost(cfg)
}
