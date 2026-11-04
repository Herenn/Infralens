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

// DriftNotifier delivers a drift report somewhere a person will see it.
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
// notifications: a dependency never seen before appeared, or an established
// one stopped being observed.
//
// It keeps the set of known dependencies in memory and seeds it from history
// at startup, so a restart neither re-announces the whole graph nor needs any
// state of its own persisted - history already is that state. Only
// connections are tracked: a new service with no new edges changes nothing
// about what depends on what, and services without a stable identity (an
// unresolved pod IP, say) would otherwise announce every reschedule.
type DriftDetector struct {
	history   storage.HistoryRepository
	retention time.Duration
	maxGap    time.Duration
	cfg       DriftConfig
	notifier  DriftNotifier
	now       func() time.Time

	known         map[string]*driftEdgeState
	lastCheck     time.Time
	baselineUntil time.Time

	// latestObserved is the newest sighting of any edge. Disappearance is
	// measured against it rather than the wall clock: if every agent stops
	// reporting, nothing is observed, latestObserved stops advancing, and
	// the whole graph is not announced as gone because of an outage on the
	// collection side.
	latestObserved time.Time
}

// NewDriftDetector creates a detector over this service's history. History
// must be enabled - drift is computed from it.
func (ts *TopologyService) NewDriftDetector(cfg DriftConfig, notifier DriftNotifier) (*DriftDetector, error) {
	if !ts.historyEnabled {
		return nil, ErrHistoryDisabled
	}
	if notifier == nil {
		return nil, fmt.Errorf("drift detector needs a notifier")
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
		history:   ts.store.History(),
		retention: ts.historyRetention,
		maxGap:    ts.historyMaxGap,
		cfg:       cfg,
		notifier:  notifier,
		now:       time.Now,
		known:     make(map[string]*driftEdgeState),
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

	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report, err := d.check(ctx)
			if err != nil {
				log.WithError(err).Warn("Drift detection check failed")
				continue
			}
			if report.Empty() {
				continue
			}
			if err := d.notifier.NotifyDrift(ctx, report); err != nil {
				// Not retried: a report is a summary of one window, and
				// replaying a stale one after the endpoint recovers would
				// be more confusing than the gap. The change is still
				// visible in the diff view.
				log.WithError(err).WithFields(log.Fields{
					"appeared":    len(report.Appeared),
					"disappeared": len(report.Disappeared),
				}).Warn("Drift detection: failed to deliver notification")
				continue
			}
			log.WithFields(log.Fields{
				"appeared":    len(report.Appeared),
				"disappeared": len(report.Disappeared),
			}).Info("Drift detection: notification sent")
		}
	}
}

// seed loads every dependency still within retention as already known.
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

	intervals, err := d.history.ConnectionsBetween(ctx, now.Add(-d.retention), now)
	if err != nil {
		return fmt.Errorf("loading known connections: %w", err)
	}
	for _, ci := range intervals {
		d.observe(ci)
	}

	// Anything that had already gone quiet before startup is not news -
	// either it was announced by the previous process or it went away
	// before detection was turned on.
	for _, st := range d.known {
		if st.edge.LastSeen.Before(d.latestObserved.Add(-d.cfg.GoneAfter)) {
			st.goneReported = true
		}
	}

	d.lastCheck = now
	return nil
}

// observe folds one history interval into the known set, returning the
// state and whether the edge was previously unknown.
func (d *DriftDetector) observe(ci storage.ConnectionInterval) (*driftEdgeState, bool) {
	if ci.LastSeen.After(d.latestObserved) {
		d.latestObserved = ci.LastSeen
	}

	st, ok := d.known[ci.ConnectionID]
	if !ok {
		st = &driftEdgeState{edge: DriftEdge{
			ConnectionID: ci.ConnectionID,
			SourceID:     ci.SourceID,
			TargetID:     ci.TargetID,
			Port:         ci.Port,
			Protocol:     ci.Protocol,
			FirstSeen:    ci.FirstSeen,
			LastSeen:     ci.LastSeen,
			External:     isPublicServiceID(ci.TargetID),
		}}
		d.known[ci.ConnectionID] = st
		return st, true
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
	intervals, err := d.history.ConnectionsBetween(ctx, d.lastCheck.Add(-d.maxGap), now)
	if err != nil {
		return nil, fmt.Errorf("loading recent connections: %w", err)
	}

	report := &DriftReport{DetectedAt: now}

	for _, ci := range intervals {
		st, isNew := d.observe(ci)
		if isNew {
			if d.reportable(st.edge) && !st.edge.FirstSeen.Before(d.baselineUntil) {
				report.Appeared = append(report.Appeared, st.edge)
			}
			continue
		}
		// Seen again after being announced as gone: re-arm it so a later
		// disappearance is announced too.
		if st.goneReported && !st.edge.LastSeen.Before(d.latestObserved.Add(-d.cfg.GoneAfter)) {
			st.goneReported = false
		}
	}

	goneBefore := d.latestObserved.Add(-d.cfg.GoneAfter)
	evictBefore := now.Add(-d.retention)
	for id, st := range d.known {
		if st.edge.LastSeen.Before(evictBefore) {
			// Out of retention: history has forgotten it, so forget it too,
			// keeping this in agreement with what a fresh seed would load.
			delete(d.known, id)
			continue
		}
		if !st.goneReported && st.edge.LastSeen.Before(goneBefore) {
			st.goneReported = true
			if d.reportable(st.edge) {
				report.Disappeared = append(report.Disappeared, st.edge)
			}
		}
	}

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
