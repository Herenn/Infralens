package demo

import (
	"context"
	"fmt"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// The demo's one story.
//
// A drift feed that is empty until something changes, an idle-spend panel
// with nothing idle and a timeline with nothing on it make a poor demo, and
// the real thing takes weeks. So the simulator gives the demo a past and a
// short scripted future, and every feature reads the same one:
//
//   - Backfilled history: two weeks of topology, written into history at
//     startup (TopologyService.ImportHistory, one interval per span), in which
//     a few dependencies come and go:
//   - -14d .. -10d  batch-exporter publishes to Kafka, then goes quiet for
//     good (a decommission candidate);
//   - -14d .. -9d   nginx routes to reports-v1, then stops (reports-v1 and
//     its MySQL on the legacy node become idle spend);
//   - -14d .. -4d6h api-gateway calls admin-panel, then stops;
//   - -14d .. -50h  orders-service uses inventory-v1, which is retired;
//   - -14d .. -31h  payment-worker uses RabbitMQ ...
//   - -30h ..       ... and moves to Stripe (api.stripe.com);
//   - -5h  ..       orders-service adopts the new search (Elasticsearch
//     8.13); the old search-v1 has had no connections all along;
//   - -50m ..       auth-service adds "Sign in with Google".
//   - Seeded events: the feed entries a detector running through those two
//     weeks would have written (with production timings: 24h before "gone"),
//     derived from the same spans, so every event matches the topology time
//     travel shows at that moment and the idle-spend evidence ("last called 9
//     days ago") matches the feed ("nginx -> reports-v1 gone").
//   - A live script: within the first minutes services start calling new
//     things - two of them on the public internet - and one is retired. The
//     real detector picks these up; nothing here writes those events. The
//     script's actors are never idle-spend candidates.
//
// Every external destination carries the DNS name its caller resolved, in
// history as well as live, so the map, time travel, the feed and exports
// all say "api.stripe.com" rather than 198.51.100.20.
//
// The past is written only on an empty history, so restarting a demo
// against a kept database neither duplicates nor moves it; the script is
// then applied up front (fastForwardScript) instead of replayed.

const (
	// backfillSpan is how far back the simulated past goes: long enough for
	// idle spend's "quiet for 9 days" and the decommission list, and for the
	// timeline scrubber to show days of history.
	backfillSpan = 14 * 24 * time.Hour

	// seededGoneAfter is the "gone" delay seeded events are written with -
	// the production default, so the story reads like a real deployment.
	seededGoneAfter = service.DefaultDriftGoneAfter

	// seededDetectionLag is how long after the change a seeded event is
	// "detected": about one default check interval.
	seededDetectionLag = 3 * time.Minute
)

// Drift timings for demo mode. The production defaults (5m checks, 24h
// before "gone", 1h warmup) would show nothing within a demo's attention
// span. GoneAfter is clamped up to HISTORY_MAX_GAP by the detector.
const (
	demoDriftInterval  = 15 * time.Second
	demoDriftGoneAfter = 5 * time.Minute
)

// TuneDriftConfig applies demo-mode drift timings to any setting not
// configured explicitly.
func TuneDriftConfig(cfg service.DriftConfig) service.DriftConfig {
	if cfg.Interval <= 0 {
		cfg.Interval = demoDriftInterval
	}
	if cfg.GoneAfter <= 0 {
		cfg.GoneAfter = demoDriftGoneAfter
	}
	if cfg.Warmup < 0 {
		// The backfilled past is the baseline; there is nothing to warm up.
		cfg.Warmup = 0
	}
	return cfg
}

// pastSpan is when an entity existed in the simulated past, as offsets from
// the simulator's start: from <= 0 (-backfillSpan: since the beginning) and
// until <= 0 (0: still running). Exactly one of service and connection is set.
type pastSpan struct {
	service    *storage.Service
	connection *demoConnection
	from       time.Duration
	until      time.Duration
}

// scriptStep is a change applied once, `at` after the simulator starts.
type scriptStep struct {
	at    time.Duration
	name  string
	apply func(ctx context.Context, s *Simulator)
	done  bool
	// actors are the services the step changes; see TestDemoStoryInvariants.
	actors []string
}

// buildTimeline defines the demo's past and its scripted future around the
// current scenario. svc and conn are buildScenario's constructors.
func (s *Simulator) buildTimeline(
	svc func(id, name, svcType, tech, icon, node, ip string) storage.Service,
	conn func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection,
) {
	const day = 24 * time.Hour

	// When currently running things first appeared; everything not listed
	// has been there since the start of the backfill.
	since := map[string]time.Duration{
		"10.0.3.15:9200":    -5 * time.Hour,
		"198.51.100.20:443": -30 * time.Hour,
		"198.51.100.61:443": -50 * time.Minute,

		"10.0.2.12/orders-service->10.0.3.15:9200:9200":   -5 * time.Hour,    // orders started using the new search
		"10.0.2.13/payment-worker->198.51.100.20:443:443": -30 * time.Hour,   // payments moved to Stripe
		"10.0.2.11/auth-service->198.51.100.61:443:443":   -50 * time.Minute, // "Sign in with Google"
	}
	start := func(id string) time.Duration {
		if d, ok := since[id]; ok {
			return d
		}
		return -backfillSpan
	}

	// &svc is a per-iteration copy, not a pointer into s.services - which
	// matters, because the script later edits s.services in place.
	for _, svc := range s.services {
		s.past = append(s.past, pastSpan{service: &svc, from: start(svc.ID)})
	}
	for _, c := range s.connections {
		s.past = append(s.past, pastSpan{connection: c, from: start(c.id())})
	}

	// Gone before the demo starts: history only.
	inventory := svc("10.0.2.20/inventory-v1", "inventory-v1", "application", "Java", "java", "demo-app-1", "10.0.2.20")
	rabbit := svc("10.0.3.16:5672", "rabbitmq", "message_queue", "RabbitMQ 3.12", "rabbitmq", "demo-data-1", "10.0.3.16")
	exporter := svc(idBatchExporter, "batch-exporter", "application", "Go", "go", "demo-app-1", "10.0.2.16")
	s.past = append(s.past,
		pastSpan{service: &exporter, from: -backfillSpan, until: -10 * day},
		pastSpan{connection: conn(idBatchExporter, "10.0.3.12:9092", 9092, 8, 1), from: -backfillSpan, until: -10 * day},
		pastSpan{connection: conn("10.0.1.10/nginx", idReportsV1, 8080, 6, 30), from: -backfillSpan, until: -9 * day},
		pastSpan{connection: conn("10.0.2.10/api-gateway", idAdminPanel, 3000, 2, 8), from: -backfillSpan, until: -(4*day + 6*time.Hour)},
		pastSpan{service: &inventory, from: -backfillSpan, until: -50 * time.Hour},
		pastSpan{connection: conn("10.0.2.12/orders-service", inventory.ID, 8081, 10, 40), from: -backfillSpan, until: -50 * time.Hour},
		pastSpan{service: &rabbit, from: -backfillSpan, until: -31 * time.Hour},
		pastSpan{connection: conn("10.0.2.13/payment-worker", rabbit.ID, 5672, 6, 3), from: -backfillSpan, until: -31 * time.Hour},
	)

	// The live script. The detector reports each appearance on its next
	// pass (demoDriftInterval); the retirement is reported once the edge has
	// been unobserved for GoneAfter (about 6-7 minutes in, with defaults).
	sendgrid := external("198.51.100.40")
	slack := external("198.51.100.77")
	s.script = []scriptStep{
		{at: 40 * time.Second, name: "orders-service starts sending email via SendGrid",
			actors: []string{"10.0.2.12/orders-service", sendgrid.ID},
			apply: func(ctx context.Context, s *Simulator) {
				s.addLive(ctx, &sendgrid, named(conn("10.0.2.12/orders-service", sendgrid.ID, 443, 3, 2), "api.sendgrid.com"))
			}},
		{at: 75 * time.Second, name: "recommendations is retired",
			actors: []string{"10.0.2.14/recommendations", "10.0.2.10/api-gateway"},
			apply: func(ctx context.Context, s *Simulator) {
				s.retireLive(ctx, "10.0.2.14/recommendations")
			}},
		{at: 2 * time.Minute, name: "auth-service starts publishing login events to Kafka",
			actors: []string{"10.0.2.11/auth-service", "10.0.3.12:9092"},
			apply: func(ctx context.Context, s *Simulator) {
				s.addLive(ctx, nil, conn("10.0.2.11/auth-service", "10.0.3.12:9092", 9092, 6, 1))
			}},
		{at: 3*time.Minute + 30*time.Second, name: "api-gateway starts posting alerts to Slack",
			actors: []string{"10.0.2.10/api-gateway", slack.ID},
			apply: func(ctx context.Context, s *Simulator) {
				s.addLive(ctx, &slack, named(conn("10.0.2.10/api-gateway", slack.ID, 443, 1, 1), "hooks.slack.com"))
			}},
	}
}

// seedPast backfills history and the drift feed, if history is on and still
// empty. Failures are logged and the demo carries on without a past. kept
// reports that history already held data - a database kept from an earlier
// run - in which case nothing is written.
func (s *Simulator) seedPast(ctx context.Context) (kept bool) {
	if !s.topology.HistoryEnabled() {
		return false
	}
	if _, ok, err := s.topology.GetHistoryBounds(ctx); err != nil || ok {
		if err != nil {
			log.WithError(err).Warn("Demo simulator: could not read history bounds, not backfilling")
		}
		return ok
	}
	began := time.Now()

	// ImportHistory clamps spans into the retention window; anything the
	// clamp would cut is left out of the seeded feed too, so the feed never
	// points at a moment time travel cannot show.
	earliest := s.topology.ImportHistoryEarliest()
	at := func(offset time.Duration) time.Time { return s.started.Add(offset) }

	spans := make([]service.HistorySpan, 0, len(s.past))
	for _, p := range s.past {
		from, to := at(p.from), at(p.until)
		if p.service != nil {
			svc := *p.service
			spans = append(spans, service.HistorySpan{From: from, To: to, Service: &svc})
			continue
		}
		spans = append(spans, service.HistorySpan{From: from, To: to, Connection: s.pastConnection(p.connection, to)})
	}
	if err := s.topology.ImportHistory(ctx, spans); err != nil {
		log.WithError(err).Warn("Demo simulator: failed to backfill history")
		return false
	}

	events := s.seededEvents(earliest)
	if _, err := s.topology.ImportDriftEvents(ctx, events); err != nil {
		log.WithError(err).Warn("Demo simulator: failed to seed drift events")
		return false
	}

	log.WithFields(log.Fields{
		"spans":        len(spans),
		"drift_events": len(events),
		"took":         time.Since(began).Round(time.Millisecond),
	}).Info("Demo simulator: backfilled two weeks of history")
	return false
}

// pastConnection is a demo connection as history records it, with its DNS
// names stamped as last observed at the end of the span.
func (s *Simulator) pastConnection(c *demoConnection, lastSeen time.Time) *storage.Connection {
	return &storage.Connection{
		ID: c.id(), SourceID: c.sourceID, TargetID: c.targetID, Port: c.port, Protocol: c.protocol,
		Hostnames: storage.ObservedHostnames(lastSeen, c.hostnames...),
	}
}

// seededEvents derives the drift feed from the past: an "appeared" event for
// every edge that started after the beginning of the backfill, a "gone" one
// for every edge that ended before the start. Names come from the same rule
// the live detector uses (service.DriftDisplayName over services annotated
// with their DNS names), so seeded and live events read alike.
func (s *Simulator) seededEvents(earliest time.Time) []storage.DriftEvent {
	var services []storage.Service
	var conns []storage.Connection
	for _, p := range s.past {
		if p.service != nil {
			services = append(services, *p.service)
		} else {
			conns = append(conns, *s.pastConnection(p.connection, s.started.Add(p.until)))
		}
	}
	service.AnnotateHostnames(services, conns)
	names := make(map[string]string, len(services))
	for i := range services {
		names[services[i].ID] = service.DriftDisplayName(&services[i])
	}

	var events []storage.DriftEvent
	for _, p := range s.past {
		c := p.connection
		if c == nil {
			continue
		}
		ev := storage.DriftEvent{
			ConnectionID: c.id(),
			SourceID:     c.sourceID,
			SourceName:   names[c.sourceID],
			TargetID:     c.targetID,
			TargetName:   names[c.targetID],
			Port:         c.port,
			Protocol:     c.protocol,
			External:     service.IsExternalServiceID(c.targetID),
			FirstSeen:    s.started.Add(p.from),
		}
		if p.from > -backfillSpan && !ev.FirstSeen.Before(earliest) {
			appeared := ev
			appeared.Kind = storage.DriftAppeared
			appeared.LastSeen = appeared.FirstSeen
			appeared.DetectedAt = appeared.FirstSeen.Add(seededDetectionLag)
			appeared.Fingerprint = fmt.Sprintf("demo|%s|%s", appeared.Kind, c.id())
			events = append(events, appeared)
		}
		if p.until < 0 && !s.started.Add(p.until).Before(earliest) {
			gone := ev
			gone.Kind = storage.DriftDisappeared
			gone.LastSeen = s.started.Add(p.until)
			gone.DetectedAt = gone.LastSeen.Add(seededGoneAfter + seededDetectionLag)
			gone.Fingerprint = fmt.Sprintf("demo|%s|%s", gone.Kind, c.id())
			events = append(events, gone)
		}
	}
	sortByDetection(events)
	return events
}

// sortByDetection orders events oldest first, so IDs ascend with time.
func sortByDetection(events []storage.DriftEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].DetectedAt.Before(events[j].DetectedAt)
	})
}

// fastForwardScript applies every scripted change at once, quietly. Used
// when the database was kept from an earlier run: that run already played
// the script, and replaying it would bring retired services back only to
// report them gone a second time.
func (s *Simulator) fastForwardScript(ctx context.Context) {
	log.Info("Demo simulator: kept database, resuming the topology where the previous run left off")
	for i := range s.script {
		s.script[i].done = true
		s.script[i].apply(ctx, s)
	}
}

// advanceScript applies any scripted change that is due.
func (s *Simulator) advanceScript(ctx context.Context) {
	elapsed := time.Since(s.started)
	for i := range s.script {
		step := &s.script[i]
		if step.done || elapsed < step.at {
			continue
		}
		step.done = true
		log.WithField("change", step.name).Info("Demo simulator: architecture change")
		step.apply(ctx, s)
	}
}

// addLive brings a new dependency (and, if given, its new target service)
// into the running topology.
func (s *Simulator) addLive(ctx context.Context, svc *storage.Service, c *demoConnection) {
	if svc != nil {
		s.services = append(s.services, *svc)
		live := *svc
		live.LastSeen = time.Now()
		if err := s.topology.AddOrUpdateService(ctx, &live); err != nil {
			log.WithError(err).WithField("service", svc.ID).Warn("Demo simulator failed to add service")
		}
	}
	s.connections = append(s.connections, c)
	if err := s.topology.AddConnection(ctx, &storage.Connection{
		ID: c.id(), SourceID: c.sourceID, TargetID: c.targetID, Port: c.port, Protocol: c.protocol, LastSeen: time.Now(),
		Hostnames: storage.ObservedHostnames(time.Now(), c.hostnames...),
	}); err != nil {
		log.WithError(err).WithField("connection", c.id()).Warn("Demo simulator failed to add connection")
	}
}

// retireLive removes a service and every connection touching it: it stops
// being simulated and leaves the live graph at once, as a decommissioned
// service would from the agents' point of view.
func (s *Simulator) retireLive(ctx context.Context, serviceID string) {
	kept := s.connections[:0]
	for _, c := range s.connections {
		if c.sourceID != serviceID && c.targetID != serviceID {
			kept = append(kept, c)
			continue
		}
		if err := s.topology.RemoveConnection(ctx, c.id()); err != nil {
			log.WithError(err).WithField("connection", c.id()).Warn("Demo simulator failed to remove connection")
		}
	}
	s.connections = kept

	services := s.services[:0]
	for _, svc := range s.services {
		if svc.ID != serviceID {
			services = append(services, svc)
		}
	}
	s.services = services
	if err := s.topology.RemoveService(ctx, serviceID); err != nil {
		log.WithError(err).WithField("service", serviceID).Warn("Demo simulator failed to remove service")
	}
}
