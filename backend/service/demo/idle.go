package demo

import (
	"context"
	"time"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	log "github.com/sirupsen/logrus"
)

// The idle-spend part of the demo world. A fresh demo store has no past, and
// idle detection is only meaningful against one ("nothing has called this in
// 9 days"), so the simulator backfills two weeks of history before it starts:
// the normal stack as continuously present, plus a few services whose traffic
// stopped at believable points. Everything here is simulated, like the rest
// of demo mode.

const nodeLegacy = "demo-legacy-1"

// demoHistorySpan is how much past the demo pretends to have observed.
const demoHistorySpan = 14 * 24 * time.Hour

type demoPastConnection struct {
	sourceID, targetID string
	port               uint16
	endedAgo           time.Duration
}

type demoPastService struct {
	svc      storage.Service
	endedAgo time.Duration
}

// idleDemoServices are the live services the idle-spend panel finds, plus
// one that it deliberately does not (nightly-etl, an outbound-only worker).
func idleDemoServices(svc func(id, name, svcType, tech, icon, node, ip string) storage.Service) []storage.Service {
	return []storage.Service{
		// A whole legacy node nothing uses any more: reports-v1 lost its last
		// caller 9 days ago but still holds its MySQL pool open.
		svc("10.0.4.10/reports-v1", "reports-v1", "web_server", "Java/Tomcat", "http", nodeLegacy, "10.0.4.10"),
		svc("10.0.4.11:3306", "reports-mysql", "database", "MySQL 5.7", "mysql", nodeLegacy, "10.0.4.11"),

		// Last called by the gateway 4 days ago.
		svc("10.0.2.17/admin-panel", "admin-panel", "web_server", "Node.js/Express", "nodejs", "demo-app-1", "10.0.2.17"),

		// Talks outbound only, never called: a worker, not idle. Shows up
		// under "not counted".
		svc("10.0.2.15/nightly-etl", "nightly-etl", "application", "Python", "python", "demo-app-1", "10.0.2.15"),

		// No connections at all for two weeks.
		svc("10.0.3.17:9200", "search-v1", "database", "Elasticsearch 6.8", "elasticsearch", "demo-data-1", "10.0.3.17"),
	}
}

func idleDemoConnections(conn func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection) []*demoConnection {
	return []*demoConnection{
		conn("10.0.4.10/reports-v1", "10.0.4.11:3306", 3306, 2, 6),
		// Prometheus still scrapes it - which is not anyone using it.
		conn("10.0.3.13:9090", "10.0.4.10/reports-v1", 8080, 1, 12),
		conn("10.0.2.17/admin-panel", "10.0.3.11:6379", 6379, 0.2, 0.3),
		conn("10.0.2.15/nightly-etl", "10.0.3.10:5432", 5432, 3, 25),
	}
}

func idleDemoHistory() ([]demoPastConnection, []demoPastService) {
	const day = 24 * time.Hour
	return []demoPastConnection{
		{sourceID: "10.0.1.10/nginx", targetID: "10.0.4.10/reports-v1", port: 8080, endedAgo: 9 * day},
		{sourceID: "10.0.2.10/api-gateway", targetID: "10.0.2.17/admin-panel", port: 3000, endedAgo: 4*day + 6*time.Hour},
		{sourceID: "10.0.2.16/batch-exporter", targetID: "10.0.3.12:9092", port: 9092, endedAgo: 10 * day},
	}, []demoPastService{
		// Gone quiet entirely: a decommission candidate on a node that
		// is still running (and still being paid for).
		{svc: storage.Service{ID: "10.0.2.16/batch-exporter", Name: "batch-exporter", Type: "application", Tech: "Go", Node: "demo-app-1"}, endedAgo: 10 * day},
	}
}

// backfillHistory writes the demo's past. A no-op when history is disabled.
func (s *Simulator) backfillHistory(ctx context.Context) {
	if !s.topology.HistoryEnabled() {
		return
	}
	// Once only. On a persisted store the past is already there; writing it
	// again relative to a new "now" would move every ended edge forward and,
	// after a long enough pause, open second intervals - the story would
	// change on every restart. Time simply passes instead, and the gap while
	// the demo was down is (correctly) reported as not observed.
	marker := s.pastConnections[0]
	markerID := (&demoConnection{sourceID: marker.sourceID, targetID: marker.targetID, port: marker.port, protocol: "tcp"}).id()
	if done, err := s.topology.HasConnectionHistory(ctx, markerID); err != nil || done {
		if done {
			log.Debug("Demo history already present - not backfilling again")
		}
		return
	}
	now := time.Now()
	from := now.Add(-demoHistorySpan)

	for i := range s.services {
		svc := s.services[i]
		if err := s.topology.BackfillServiceHistory(ctx, &svc, from, now); err != nil {
			log.WithError(err).Debug("Demo simulator failed to backfill service history")
		}
	}
	for _, c := range s.connections {
		if err := s.topology.BackfillConnectionHistory(ctx, &storage.Connection{
			ID: c.id(), SourceID: c.sourceID, TargetID: c.targetID, Port: c.port, Protocol: c.protocol,
		}, from, now); err != nil {
			log.WithError(err).Debug("Demo simulator failed to backfill connection history")
		}
	}
	for _, p := range s.pastServices {
		svc := p.svc
		if err := s.topology.BackfillServiceHistory(ctx, &svc, from, now.Add(-p.endedAgo)); err != nil {
			log.WithError(err).Debug("Demo simulator failed to backfill service history")
		}
	}
	for _, p := range s.pastConnections {
		c := &demoConnection{sourceID: p.sourceID, targetID: p.targetID, port: p.port, protocol: "tcp"}
		if err := s.topology.BackfillConnectionHistory(ctx, &storage.Connection{
			ID: c.id(), SourceID: c.sourceID, TargetID: c.targetID, Port: c.port, Protocol: c.protocol,
		}, from, now.Add(-p.endedAgo)); err != nil {
			log.WithError(err).Debug("Demo simulator failed to backfill connection history")
		}
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
