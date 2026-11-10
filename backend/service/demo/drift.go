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

// The demo's architecture drift story.
//
// A drift feed that is empty until something changes makes a poor demo, and
// real drift takes days. So the simulator gives the demo a past and a short
// scripted future:
//
//   - Backfilled history: three days of topology, written straight into
//     history at startup, in which a few dependencies come and go. Time
//     travel works across all of it.
//   - Seeded events: the feed entries a detector running through those three
//     days would have written (with production timings: 24h before "gone"),
//     derived from the same spans so every event matches the topology shown
//     at that moment.
//   - A live script: within the first minutes, services start calling new
//     things - two of them on the public internet - and one is retired. The
//     real detector picks these up; nothing here writes those events.
//
// Backfill and seeding only happen on an empty history, so restarting a demo
// against a kept database neither duplicates the past nor rewrites it.

const (
	// backfillSpan is how far back the simulated past goes.
	backfillSpan = 72 * time.Hour

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
}

// buildTimeline defines the demo's past and its scripted future around the
// current scenario. svc and conn are buildScenario's constructors.
func (s *Simulator) buildTimeline(
	svc func(id, name, svcType, tech, icon, node, ip string) storage.Service,
	conn func(src, dst string, port uint16, sentKBps, recvKBps float64) *demoConnection,
) {
	// When currently running things first appeared; everything not listed
	// has been there since the start of the backfill.
	since := map[string]time.Duration{
		"10.0.3.15:9200":    -5 * time.Hour,
		"198.51.100.20:443": -30 * time.Hour,
		"198.51.100.61:443": -50 * time.Minute,

		"10.0.2.12/orders-service->10.0.3.15:9200:9200":   -5 * time.Hour,    // orders started using search
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

	// Retired before the demo starts: history only.
	inventory := svc("10.0.2.20/inventory-v1", "inventory-v1", "application", "Java", "java", "demo-app-1", "10.0.2.20")
	rabbit := svc("10.0.3.16:5672", "rabbitmq", "message_queue", "RabbitMQ 3.12", "rabbitmq", "demo-data-1", "10.0.3.16")
	s.past = append(s.past,
		pastSpan{service: &inventory, from: -backfillSpan, until: -50 * time.Hour},
		pastSpan{connection: conn("10.0.2.12/orders-service", inventory.ID, 8081, 10, 40), from: -backfillSpan, until: -50 * time.Hour},
		pastSpan{service: &rabbit, from: -backfillSpan, until: -31 * time.Hour},
		pastSpan{connection: conn("10.0.2.13/payment-worker", rabbit.ID, 5672, 6, 3), from: -backfillSpan, until: -31 * time.Hour},
	)

	// The live script. The detector reports each appearance on its next
	// pass (demoDriftInterval); the retirement is reported once the edge has
	// been unobserved for GoneAfter (about 8 minutes in, with defaults).
	sendgrid := svc("198.51.100.40:443", "api.sendgrid.com", "unknown", "HTTPS", "https", "", "198.51.100.40")
	slack := svc("198.51.100.77:443", "hooks.slack.com", "unknown", "HTTPS", "https", "", "198.51.100.77")
	s.script = []scriptStep{
		{at: 40 * time.Second, name: "orders-service starts sending email via SendGrid", apply: func(ctx context.Context, s *Simulator) {
			s.addLive(ctx, &sendgrid, conn("10.0.2.12/orders-service", sendgrid.ID, 443, 3, 2))
		}},
		{at: 75 * time.Second, name: "recommendations is retired", apply: func(ctx context.Context, s *Simulator) {
			s.retireLive(ctx, "10.0.2.14/recommendations")
		}},
		{at: 2 * time.Minute, name: "auth-service starts publishing login events to Kafka", apply: func(ctx context.Context, s *Simulator) {
			s.addLive(ctx, nil, conn("10.0.2.11/auth-service", "10.0.3.12:9092", 9092, 6, 1))
		}},
		{at: 3*time.Minute + 30*time.Second, name: "api-gateway starts posting alerts to Slack", apply: func(ctx context.Context, s *Simulator) {
			s.addLive(ctx, &slack, conn("10.0.2.10/api-gateway", slack.ID, 443, 1, 1))
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

	// Comfortably inside the max gap, so each span is one continuous
	// interval rather than a string of separate sightings.
	step := s.topology.HistoryMaxGap() * 3 / 4
	if step <= 0 {
		return
	}
	began := time.Now()
	origin := s.started.Add(-backfillSpan)
	grid := func(k int) time.Time { return origin.Add(time.Duration(k) * step) }

	var obs []service.HistoryObservation
	for k := 0; grid(k).Before(s.started); k++ {
		at := grid(k)
		for _, p := range s.past {
			if at.Before(s.started.Add(p.from)) || (p.until < 0 && at.After(s.started.Add(p.until))) {
				continue
			}
			if p.service != nil {
				svc := *p.service
				obs = append(obs, service.HistoryObservation{At: at, Service: &svc})
			} else {
				c := p.connection
				obs = append(obs, service.HistoryObservation{At: at, Connection: &storage.Connection{
					ID: c.id(), SourceID: c.sourceID, TargetID: c.targetID, Port: c.port, Protocol: c.protocol,
				}})
			}
		}
	}
	if err := s.topology.ImportHistory(ctx, obs); err != nil {
		log.WithError(err).Warn("Demo simulator: failed to backfill history")
		return
	}

	// The first / last grid instants of a span: exactly what the backfill
	// recorded, so "view topology at that moment" lands where the edge is.
	firstAt := func(offset time.Duration) time.Time {
		k := int((s.started.Add(offset).Sub(origin) + step - 1) / step)
		return grid(k)
	}
	lastAt := func(offset time.Duration) time.Time {
		return grid(int(s.started.Add(offset).Sub(origin) / step))
	}

	names := make(map[string]string)
	for _, p := range s.past {
		if p.service != nil {
			names[p.service.ID] = service.DriftDisplayName(p.service)
		}
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
			FirstSeen:    firstAt(p.from),
		}
		if p.from > -backfillSpan {
			appeared := ev
			appeared.Kind = storage.DriftAppeared
			appeared.LastSeen = appeared.FirstSeen
			appeared.DetectedAt = appeared.FirstSeen.Add(seededDetectionLag)
			appeared.Fingerprint = fmt.Sprintf("demo|%s|%s", appeared.Kind, c.id())
			events = append(events, appeared)
		}
		if p.until < 0 {
			gone := ev
			gone.Kind = storage.DriftDisappeared
			gone.LastSeen = lastAt(p.until)
			gone.DetectedAt = gone.LastSeen.Add(seededGoneAfter + seededDetectionLag)
			gone.Fingerprint = fmt.Sprintf("demo|%s|%s", gone.Kind, c.id())
			events = append(events, gone)
		}
	}
	sortByDetection(events)
	if _, err := s.topology.ImportDriftEvents(ctx, events); err != nil {
		log.WithError(err).Warn("Demo simulator: failed to seed drift events")
		return
	}

	log.WithFields(log.Fields{
		"observations": len(obs),
		"drift_events": len(events),
		"took":         time.Since(began).Round(time.Millisecond),
	}).Info("Demo simulator: backfilled three days of history")
	return false
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
