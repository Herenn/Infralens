package service

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/storage"
)

// Drift detection defaults. See DriftConfig for what each one trades off.
const (
	DefaultDriftCheckInterval = 5 * time.Minute
	DefaultDriftGoneAfter     = 24 * time.Hour
	DefaultDriftWarmup        = time.Hour

	// maxDriftItems caps how many changes of each kind one notification
	// lists. The totals are always reported in full; only the itemized lists
	// are truncated, so a large rollout produces one readable message rather
	// than one a chat client refuses to render.
	maxDriftItems = 25
)

// DriftConfig configures continuous architecture drift detection.
type DriftConfig struct {
	// Interval is how often history is checked for changes.
	Interval time.Duration

	// GoneAfter is how long a dependency must go unobserved before it is
	// reported as gone. Deliberately long by default: this is architecture
	// drift, not outage alerting, and a dependency exercised by a nightly job
	// must not be announced as gone and back again every day.
	GoneAfter time.Duration

	// Warmup suppresses "new dependency" reports for edges first observed
	// within this long of the earliest recorded history. On a fresh install
	// every edge is new; announcing the whole cluster as drift on day one
	// would teach people to ignore the notifications before they mean
	// anything. Negative means DefaultDriftWarmup; zero disables warmup.
	Warmup time.Duration
}

// DriftEdge is one dependency (connection) that appeared or disappeared.
type DriftEdge struct {
	ConnectionID string    `json:"connection_id"`
	SourceID     string    `json:"source_id"`
	TargetID     string    `json:"target_id"`
	Port         uint16    `json:"port"`
	Protocol     string    `json:"protocol,omitempty"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`

	// External is true when the target is a public internet address - "a
	// service started talking to the internet" is the drift people most
	// want to hear about, so it is flagged rather than left to be inferred
	// from an IP.
	External bool `json:"external"`

	// TargetHostname is the DNS name the source resolved the target from,
	// when the agent observed one: "api.stripe.com" rather than 52.1.2.3.
	TargetHostname string `json:"target_hostname,omitempty"`
}

// DriftReport is everything one detection pass found.
type DriftReport struct {
	DetectedAt  time.Time   `json:"detected_at"`
	Appeared    []DriftEdge `json:"appeared"`
	Disappeared []DriftEdge `json:"disappeared"`
}

// Empty reports whether the pass found nothing worth notifying about.
func (r *DriftReport) Empty() bool {
	return len(r.Appeared) == 0 && len(r.Disappeared) == 0
}

// DriftNotifier delivers a drift report somewhere a person will see it, in
// addition to the persisted feed. Optional: without one, drift is still
// recorded and pushed to open UIs.
type DriftNotifier interface {
	NotifyDrift(ctx context.Context, report *DriftReport) error
}

// driftEdgeState is what the detector remembers about one dependency.
type driftEdgeState struct {
	edge DriftEdge

	// goneReported marks an edge already announced as gone, so it is
	// announced once per disappearance rather than on every pass.
	goneReported bool
}

// DriftDetector turns recorded topology history into "something changed"
// events: a dependency never seen before appeared, or an established one
// stopped being observed. Each change is persisted to the drift feed (one
// event per edge), pushed to open UIs over the event bus, and - if a
// notifier is configured - sent on as a report.
//
// It keeps the set of known dependencies in memory and seeds it from history
// at startup, so a restart does not re-announce the whole graph - history
// already is that state. The only state of its own it persists is a
// checkpoint of the last completed pass, so a restart resumes from there
// instead of silently absorbing changes that happened between that pass and
// shutdown into its baseline (see seed). Re-detecting a change is harmless:
// events are deduplicated by fingerprint on insert, and only what was
// actually new is published or notified.
//
// Only connections are tracked: a new service with no new edges changes
// nothing about what depends on what, and services without a stable
// identity (an unresolved pod IP, say) would otherwise announce every
// reschedule.
type DriftDetector struct {
	ts        *TopologyService
	history   storage.HistoryRepository
	events    storage.DriftRepository
	retention time.Duration
	maxGap    time.Duration
	cfg       DriftConfig
	notifier  DriftNotifier // nil: feed and live push only
	now       func() time.Time

	known         map[string]*driftEdgeState
	lastCheck     time.Time
	baselineUntil time.Time

	// cov is when each node's agent was observing (coverage.go, shared with
	// Idle spend). An edge's absence is measured in time during which an
	// agent that could have recorded it was reporting: an outage - of the
	// backend, of collection, or of just the one node's agent - is not
	// absence, so recovering from it does not announce every edge that has
	// not happened to be seen again yet.
	cov *observationCoverage

	// nodes maps service IDs to their node, for edgeReporters. Filled from
	// history at seed and from current state on every pass.
	nodes map[string]string

	// silent: agents that stopped reporting within the retirement window,
	// as of the last pass (see silentAgents), for the status.
	silent []silentAgent

	// pending holds events that could not be written to the feed yet.
	pending []storage.DriftEvent
}

// NewDriftDetector creates a detector over this service's history. History
// must be enabled - drift is computed from it. notifier may be nil: changes
// are then only recorded in the feed and pushed to open UIs.
func (ts *TopologyService) NewDriftDetector(cfg DriftConfig, notifier DriftNotifier) (*DriftDetector, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultDriftCheckInterval
	}
	if cfg.GoneAfter <= 0 {
		cfg.GoneAfter = DefaultDriftGoneAfter
	}
	// A GoneAfter inside maxGap would announce an edge as gone while history
	// itself still considers its interval open.
	if cfg.GoneAfter < ts.historyMaxGap {
		cfg.GoneAfter = ts.historyMaxGap
	}
	if cfg.Warmup < 0 {
		cfg.Warmup = DefaultDriftWarmup
	}
	return &DriftDetector{
		ts:        ts,
		history:   ts.store.History(),
		events:    ts.store.Drift(),
		retention: ts.historyRetention,
		maxGap:    ts.historyMaxGap,
		cfg:       cfg,
		notifier:  notifier,
		now:       time.Now,
		known:     make(map[string]*driftEdgeState),
		nodes:     make(map[string]string),
	}, nil
}

// Run seeds the detector from history and then checks for drift every
// Interval until ctx is cancelled.
func (d *DriftDetector) Run(ctx context.Context) {
	// Retry seeding rather than giving up: without a baseline every check
	// would report the entire graph as new.
	for {
		err := d.seed(ctx)
		if err == nil {
			break
		}
		log.WithError(err).Warn("Drift detection: failed to load baseline from history, retrying")
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.cfg.Interval):
		}
	}

	d.publishStatus()

	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.step(ctx)
		}
	}
}

// step runs one detection pass and delivers what it found: persisted to the
// feed, pushed to open UIs, and sent to the notifier if there is one.
func (d *DriftDetector) step(ctx context.Context) {
	report, err := d.check(ctx)
	if err != nil {
		log.WithError(err).Warn("Drift detection check failed")
		return
	}
	report = d.persist(ctx, report)
	d.publishStatus()

	if report.Empty() || d.notifier == nil {
		return
	}
	if err := d.notifier.NotifyDrift(ctx, report); err != nil {
		// Not retried: a report is a summary of one window, and replaying a
		// stale one after the endpoint recovers would be more confusing than
		// the gap. The change is still in the feed and the diff view.
		log.WithError(err).WithFields(log.Fields{
			"appeared":    len(report.Appeared),
			"disappeared": len(report.Disappeared),
		}).Warn("Drift detection: failed to deliver notification")
		return
	}
	log.WithFields(log.Fields{
		"appeared":    len(report.Appeared),
		"disappeared": len(report.Disappeared),
	}).Info("Drift detection: notification sent")
}

// maxPendingDriftEvents bounds the events kept for retry while the feed
// cannot be written. Beyond it the oldest are dropped from memory - but not
// lost for good: the checkpoint does not advance while anything is pending,
// so a restart resumes from before them and records them then.
const maxPendingDriftEvents = 10000

// persist records a pass's changes in the drift feed, publishes the newly
// recorded ones, advances the checkpoint, and returns the part of the report
// that is actually news.
//
// A change already in the feed - re-detected because a restart resumed from
// an earlier checkpoint, or because another replica got there first - is
// dropped from the returned report, so it is neither pushed nor notified a
// second time.
//
// If the feed cannot be written, the pass's events are kept pending and
// retried on every later pass, the checkpoint stays where it was until they
// are recorded, and the full report is returned so the notifier hears about
// it now (once: a later successful retry publishes the events to open UIs
// but does not notify again).
func (d *DriftDetector) persist(ctx context.Context, report *DriftReport) *DriftReport {
	if d.events == nil {
		return report
	}

	fresh := d.eventsFor(ctx, report)
	batch := fresh
	if len(d.pending) > 0 {
		batch = append(append([]storage.DriftEvent(nil), d.pending...), fresh...)
	}
	inserted, err := d.events.InsertEvents(ctx, batch)
	if err != nil {
		d.keepPending(fresh)
		log.WithError(err).WithFields(log.Fields{
			"appeared":    len(report.Appeared),
			"disappeared": len(report.Disappeared),
			"pending":     len(d.pending),
		}).Warn("Drift detection: failed to record changes in the feed, will retry")
		return report
	}
	d.pending = nil
	if err := d.events.SetCheckpoint(ctx, report.DetectedAt); err != nil {
		log.WithError(err).Warn("Drift detection: failed to record checkpoint")
	}
	if len(inserted) > 0 {
		d.ts.publishDriftEvents(inserted)
	}

	news := make(map[string]bool, len(inserted))
	for _, ev := range inserted {
		news[ev.Fingerprint] = true
	}
	out := &DriftReport{DetectedAt: report.DetectedAt}
	for _, e := range report.Appeared {
		if news[driftFingerprint(storage.DriftAppeared, e)] {
			out.Appeared = append(out.Appeared, e)
		}
	}
	for _, e := range report.Disappeared {
		if news[driftFingerprint(storage.DriftDisappeared, e)] {
			out.Disappeared = append(out.Disappeared, e)
		}
	}
	return out
}

// keepPending queues events for retry, dropping the oldest beyond the bound.
func (d *DriftDetector) keepPending(events []storage.DriftEvent) {
	d.pending = append(d.pending, events...)
	if over := len(d.pending) - maxPendingDriftEvents; over > 0 {
		log.WithField("dropped", over).Warn("Drift detection: too many unrecorded changes; " +
			"dropping the oldest from memory (a restart will re-detect them)")
		d.pending = append([]storage.DriftEvent(nil), d.pending[over:]...)
	}
}

// maxSilenceThreshold caps how long nothing may be observed on a node's
// edges before, in history recorded before agent heartbeats existed, the
// stretch counts as a collection outage. A node with anything running
// produces observations every few minutes (throughput reports refresh every
// live connection's history), so an hour of silence is an outage, not a
// quiet spell.
const maxSilenceThreshold = time.Hour

// silenceThreshold is how long after an edge interval ends, in history from
// before agent heartbeats, the node is still assumed to be observing: half
// of GoneAfter, but at least the history max gap (shorter gaps are ordinary
// cadence) and at most maxSilenceThreshold. It only ever errs towards
// reporting "gone" later, never falsely.
func (d *DriftDetector) silenceThreshold() time.Duration {
	t := d.cfg.GoneAfter / 2
	if t > maxSilenceThreshold {
		t = maxSilenceThreshold
	}
	if t < d.maxGap {
		t = d.maxGap
	}
	return t
}

// Retirement (see coverage.go): a node whose own agent has been silent this
// long while InfraLens keeps observing elsewhere is treated as gone for good
// - decommissioned, replaced, scaled in - rather than down, so its edges are
// reported gone (once) instead of never. Three GoneAfters, so an outage has
// to last well past the point where a disappearance would have been
// reported before it counts, with a six-hour floor so a short GoneAfter
// (demo mode, tests, an impatient operator) cannot turn a routine reboot or
// maintenance window into a retirement. With the default 24h GoneAfter: 72h.
const (
	driftRetireMultiple = 3
	driftRetireFloor    = 6 * time.Hour
)

func (d *DriftDetector) retireAfter() time.Duration {
	r := driftRetireMultiple * d.cfg.GoneAfter
	if r < driftRetireFloor {
		r = driftRetireFloor
	}
	return r
}

// see accounts for one interval as evidence of its reporting agents'
// observation (see coverage.go). Keyed by interval, so an interval read
// again after being extended replaces its span.
func (d *DriftDetector) see(ci storage.ConnectionInterval) {
	key := fmt.Sprintf("%d|%s", ci.ID, ci.ConnectionID)
	if ci.ID == 0 {
		key = ci.ConnectionID + "|" + ci.FirstSeen.String()
	}
	d.cov.addEvidence(key, edgeEvidenceNodes(ci.SourceID, ci.TargetID, d.nodeOf), ci.FirstSeen, ci.LastSeen)
}

func (d *DriftDetector) nodeOf(id string) string { return d.nodes[id] }

// loadCoverage refreshes what the coverage needs from the store: every agent
// heartbeat within retention, the CLUSTER_NAME groups agents report under
// now, and the node of every current service. Node
// mappings of services that left current state are kept only while a known
// edge still refers to them, so the map is bounded by current state plus
// the known edges.
func (d *DriftDetector) loadCoverage(ctx context.Context, now time.Time, prune bool) error {
	if d.cov == nil {
		d.cov = newObservationCoverage(d.maxGap, d.silenceThreshold(), d.retireAfter())
	}
	beats, err := d.history.NodesBetween(ctx, now.Add(-d.retention), now)
	if err != nil {
		return fmt.Errorf("loading node coverage: %w", err)
	}
	d.cov.now = d.now
	d.cov.setHeartbeats(beats)
	groups := make([]string, 0)
	for g := range d.ts.agentGroups(now) {
		groups = append(groups, g)
	}
	d.cov.setGroups(groups)
	d.silent = d.cov.silentAgents(now.Add(-d.retireAfter()), now, false)
	services, err := d.ts.store.Services().List(ctx, storage.ServiceFilter{})
	if err != nil {
		return fmt.Errorf("loading service nodes: %w", err)
	}
	nodes := make(map[string]string, len(services))
	if !prune { // seeding: the known set is being built from these
		nodes = d.nodes
	}
	for _, st := range d.known {
		for _, id := range []string{st.edge.SourceID, st.edge.TargetID} {
			if n, ok := d.nodes[id]; ok {
				nodes[id] = n
			}
		}
	}
	for _, svc := range services {
		if svc.Node != "" {
			nodes[svc.ID] = svc.Node
		}
	}
	d.nodes = nodes
	return nil
}

// gone reports whether an edge has gone unobserved for GoneAfter of observed
// time, as of ref - observed by the agent(s) that could have recorded it
// (edgeReporters).
func (d *DriftDetector) gone(st *driftEdgeState, ref time.Time) bool {
	reporters := edgeReporters(st.edge.SourceID, st.edge.TargetID, d.nodeOf)
	return d.cov.observed(reporters, st.edge.LastSeen, ref) > d.cfg.GoneAfter
}

// seed loads the detector's baseline from history.
//
// Without a checkpoint (first start, or upgrading from a version without a
// feed) every dependency still within retention is loaded as already known:
// nothing that predates detection is announced.
//
// With one, the detector resumes: it rebuilds the state it would have had at
// the checkpoint and lets the first pass cover everything since. Seeding
// "everything is known" at every start would swallow any change that
// happened between the last pass and shutdown - an edge that appeared in the
// final minutes before a restart would never be reported, and on a backend
// that is redeployed often that is not a rare window.
//
// History is streamed in first_seen order rather than loaded: the window is
// the whole retention period. That order is also what lets one pass tell an
// edge's first sighting apart from its later ones. Observation coverage
// (coverage.go) is loaded first and fed the same intervals.
//
// Edges that can never be reported - inbound connections from public
// internet clients, one per visitor - are not kept at all.
//
// Resuming: edges first seen within maxGap before the checkpoint are left
// unknown. The pass at the checkpoint may not have seen them - history is
// written with the observation's own timestamp, which can trail when it
// becomes visible (the same reason check overlaps its windows) - so the first
// pass re-evaluates them, and the feed's fingerprint deduplication absorbs
// the ones that were in fact already reported. Edges already gone at the
// checkpoint are marked reported, judged against the newest observation as
// of the checkpoint. That is reconstructed approximately (intervals are
// extended in place) and deliberately underestimated by maxGap, so an edge on
// the boundary is re-evaluated and deduplicated rather than wrongly assumed
// announced.
func (d *DriftDetector) seed(ctx context.Context) error {
	now := d.now()

	earliest := now
	bounds, ok, err := d.history.Bounds(ctx)
	if err != nil {
		return fmt.Errorf("reading history bounds: %w", err)
	}
	if ok && bounds.Earliest.Before(now) {
		earliest = bounds.Earliest
	}
	d.baselineUntil = earliest.Add(d.cfg.Warmup)

	checkpoint, resuming := d.resumePoint(ctx, now)
	pendingAfter := checkpoint.Add(-d.maxGap)
	var observedAtCheckpoint time.Time

	d.known = make(map[string]*driftEdgeState)
	d.cov = nil
	d.nodes = make(map[string]string)
	// Nodes of services only history still knows (pruned from current
	// state), then current state on top, then heartbeats - all before
	// streaming: with SQLite's single connection nothing else can query
	// while EachConnectionBetween iterates.
	past, err := d.history.ServicesBetween(ctx, now.Add(-d.retention), now)
	if err != nil {
		return fmt.Errorf("loading service history: %w", err)
	}
	for _, si := range past {
		if si.Node != "" {
			d.nodes[si.ServiceID] = si.Node
		}
	}
	if err := d.loadCoverage(ctx, now, false); err != nil {
		return err
	}
	err = d.history.EachConnectionBetween(ctx, now.Add(-d.retention), now, func(ci storage.ConnectionInterval) error {
		d.see(ci)
		if resuming {
			if !ci.FirstSeen.After(checkpoint) {
				seen := ci.LastSeen
				if seen.After(checkpoint) {
					seen = checkpoint
				}
				if seen.After(observedAtCheckpoint) {
					observedAtCheckpoint = seen
				}
			}
			// First sighting (first_seen order) after the cutoff: left for
			// the first pass, and so are its later intervals.
			if _, known := d.known[ci.ConnectionID]; !known && ci.FirstSeen.After(pendingAfter) {
				return nil
			}
		}
		d.observe(ci)
		return nil
	})
	if err != nil {
		return fmt.Errorf("loading known connections: %w", err)
	}

	// Anything that had already gone quiet is not news - either it was
	// announced by the previous process or it went away before detection
	// was turned on.
	ref := now
	if resuming {
		ref = observedAtCheckpoint.Add(-d.maxGap)
	}
	if !ref.IsZero() {
		for _, st := range d.known {
			if d.gone(st, ref) {
				st.goneReported = true
			}
		}
	}

	d.lastCheck = now
	if resuming {
		d.lastCheck = checkpoint
	}
	return nil
}

// resumePoint returns the checkpoint to resume from, if there is a usable
// one. One older than retention is ignored: history no longer holds what
// happened since, and resuming from it would announce everything first seen
// in the window as new. One in the future - left by a clock that jumped
// forward and back - is reset to now; otherwise it would silently disable
// resuming until real time caught up with it.
func (d *DriftDetector) resumePoint(ctx context.Context, now time.Time) (time.Time, bool) {
	if d.events == nil {
		return time.Time{}, false
	}
	at, ok, err := d.events.Checkpoint(ctx)
	if err != nil {
		// Fall back to the conservative baseline rather than failing startup:
		// at worst a change from just before the restart goes unreported.
		log.WithError(err).Warn("Drift detection: could not read checkpoint, starting from current history")
		return time.Time{}, false
	}
	if !ok || at.Before(now.Add(-d.retention)) {
		return time.Time{}, false
	}
	if at.After(now) {
		log.WithField("checkpoint", at).Warn("Drift detection: checkpoint is in the future (clock change?), resetting it to now")
		if err := d.events.ResetCheckpoint(ctx, now); err != nil {
			log.WithError(err).Warn("Drift detection: could not reset checkpoint")
		}
		at = now
	}
	return at, true
}

// observe folds one history interval into the known set, returning the
// state and whether the edge was previously unknown. Edges that can never be
// reported are not kept: the result is nil.
func (d *DriftDetector) observe(ci storage.ConnectionInterval) (*driftEdgeState, bool) {
	st, ok := d.known[ci.ConnectionID]
	if !ok {
		if isPublicServiceID(ci.SourceID) {
			return nil, false // see reportable
		}
		st = &driftEdgeState{edge: DriftEdge{
			ConnectionID: ci.ConnectionID,
			SourceID:     ci.SourceID,
			TargetID:     ci.TargetID,
			Port:         ci.Port,
			Protocol:     ci.Protocol,
			FirstSeen:    ci.FirstSeen,
			LastSeen:     ci.LastSeen,
			External:     isPublicServiceID(ci.TargetID),

			TargetHostname: edgeHostname(ci.TargetID, ci.Hostnames),
		}}
		d.known[ci.ConnectionID] = st
		return st, true
	}
	// A name can arrive after the edge was first recorded.
	if h := edgeHostname(ci.TargetID, ci.Hostnames); h != "" {
		st.edge.TargetHostname = h
	}
	if ci.FirstSeen.Before(st.edge.FirstSeen) {
		st.edge.FirstSeen = ci.FirstSeen
	}
	if ci.LastSeen.After(st.edge.LastSeen) {
		st.edge.LastSeen = ci.LastSeen
	}
	return st, false
}

// check runs one detection pass over what history recorded since the last.
func (d *DriftDetector) check(ctx context.Context) (*DriftReport, error) {
	now := d.now()

	// Overlap the previous window by maxGap: history is written with the
	// observation's own timestamp, which can trail the moment it becomes
	// visible to this query. Re-reading a little is free - known edges are
	// deduplicated - while missing a write would lose a new edge for good.
	if err := d.loadCoverage(ctx, now, true); err != nil {
		return nil, err
	}
	var intervals []storage.ConnectionInterval
	err := d.history.EachConnectionBetween(ctx, d.lastCheck.Add(-d.maxGap), now, func(ci storage.ConnectionInterval) error {
		intervals = append(intervals, ci)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading recent connections: %w", err)
	}

	report := &DriftReport{DetectedAt: now}

	for _, ci := range intervals {
		d.see(ci)
		st, isNew := d.observe(ci)
		if st == nil {
			continue
		}
		if isNew {
			if !st.edge.FirstSeen.Before(d.baselineUntil) {
				report.Appeared = append(report.Appeared, st.edge)
			}
			continue
		}
		// Seen again after being announced as gone: re-arm it so a later
		// disappearance is announced too.
		if st.goneReported && !d.gone(st, now) {
			st.goneReported = false
		}
	}

	evictBefore := now.Add(-d.retention)
	for id, st := range d.known {
		if st.edge.LastSeen.Before(evictBefore) {
			// Out of retention: history has forgotten it, so forget it too,
			// keeping this in agreement with what a fresh seed would load.
			delete(d.known, id)
			continue
		}
		if !st.goneReported && d.gone(st, now) {
			st.goneReported = true
			if d.reportable(st.edge) {
				report.Disappeared = append(report.Disappeared, st.edge)
			}
		}
	}
	d.cov.forget(evictBefore)

	sortDriftEdges(report.Appeared)
	sortDriftEdges(report.Disappeared)

	d.lastCheck = now
	return report, nil
}

// reportable filters out edges whose churn is not architecture drift: inbound
// connections from public internet clients. Every new visitor to an
// internet-facing service is a new source IP and so a new edge; announcing
// them would bury the changes that matter.
func (d *DriftDetector) reportable(e DriftEdge) bool {
	return !isPublicServiceID(e.SourceID)
}

func sortDriftEdges(edges []DriftEdge) {
	sort.Slice(edges, func(i, j int) bool {
		// External first: those are the ones most worth reading.
		if edges[i].External != edges[j].External {
			return edges[i].External
		}
		return edges[i].ConnectionID < edges[j].ConnectionID
	})
}

// isPublicServiceID reports whether a service ID identifies a public internet
// address.
//
// Service IDs for anything not resolved to a Kubernetes resource are built
// from the peer's IP: a bare "IP", "IP:port" for an outbound destination, or
// "IP/process" for a local listener (see the makeServiceID helpers in
// processor.go). Resolved Kubernetes IDs don't parse as an IP and are never
// external. Private, loopback, link-local and carrier-grade NAT ranges
// (100.64.0.0/10, common for overlay networks) count as internal.
func isPublicServiceID(id string) bool {
	ip := leadingIP(id)
	if ip == nil {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return false
	}
	return true
}

func leadingIP(id string) net.IP {
	if ip := net.ParseIP(id); ip != nil {
		return ip
	}
	if i := strings.Index(id, "/"); i > 0 {
		if ip := net.ParseIP(id[:i]); ip != nil {
			return ip
		}
	}
	if i := strings.LastIndex(id, ":"); i > 0 {
		host := strings.TrimSuffix(strings.TrimPrefix(id[:i], "["), "]")
		if ip := net.ParseIP(host); ip != nil {
			return ip
		}
	}
	return nil
}
