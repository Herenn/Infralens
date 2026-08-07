package storage

import (
	"context"
	"time"
)

// ServiceInterval is a contiguous period during which a service was observed.
//
// Services and connections are stored as current state only, and pruned by
// hard delete. Intervals are the historical record: rather than a row per
// observation, one row covers the whole stretch of time an entity was
// continuously present, with LastSeen extended in place as it keeps being
// seen. That keeps write volume proportional to how often the architecture
// changes rather than how often agents report.
type ServiceInterval struct {
	ID        int64     `json:"id"`
	ServiceID string    `json:"service_id"`
	Name      string    `json:"name,omitempty"`
	Type      string    `json:"type,omitempty"`
	Tech      string    `json:"tech,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
	Node      string    `json:"node,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// ConnectionInterval is a contiguous period during which an edge was observed.
type ConnectionInterval struct {
	ID           int64     `json:"id"`
	ConnectionID string    `json:"connection_id"`
	SourceID     string    `json:"source_id"`
	TargetID     string    `json:"target_id"`
	Port         uint16    `json:"port"`
	Protocol     string    `json:"protocol,omitempty"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

// HistoryBounds is the earliest and latest instants covered by recorded
// history. It sizes a UI timeline control without the caller having to guess
// a retention window or scan every interval itself.
type HistoryBounds struct {
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
}

// HistoryRepository records and reconstructs topology over time.
//
// Recording is "extend or open": an observation whose entity already has an
// interval ending no longer than maxGap ago extends that interval, otherwise a
// new one begins. maxGap is what separates "still running, we just haven't had
// an event for a moment" from "this went away and came back" - too small and a
// brief agent restart looks like the service disappearing, too large and a
// genuine outage is invisible.
type HistoryRepository interface {
	// RecordService extends or opens an interval for svc, observed at `at`.
	RecordService(ctx context.Context, svc *Service, at time.Time, maxGap time.Duration) error

	// RecordConnection extends or opens an interval for conn, observed at `at`.
	RecordConnection(ctx context.Context, conn *Connection, at time.Time, maxGap time.Duration) error

	// ServicesAt returns the services present at a point in time.
	//
	// grace extends how long after its last observation an interval still
	// counts as covering an instant. Without it, nothing is ever "present
	// now": last_seen is the last time an entity was *observed*, always
	// slightly in the past, so a strict `last_seen >= at` excludes everything
	// currently alive. Pass the same maxGap used when recording - that is
	// already the window within which a sighting extends the open interval
	// rather than starting a new one, so using it here makes reads agree with
	// writes about when an interval is still open.
	ServicesAt(ctx context.Context, at time.Time, grace time.Duration) ([]ServiceInterval, error)

	// ConnectionsAt returns the edges present at a point in time. See
	// ServicesAt for grace.
	ConnectionsAt(ctx context.Context, at time.Time, grace time.Duration) ([]ConnectionInterval, error)

	// ServicesBetween returns every service interval overlapping [from, to],
	// one row per interval.
	//
	// Deliberately NOT merged per service: over a window, separate intervals
	// are the answer. Collapsing them into one spanning row would erase the
	// gaps - the absences this model exists to record - and report a service
	// as continuously present across a week it was gone.
	//
	// No production caller as of v3 - ?from=&to= on /topology was scaffolded
	// on top of this but never wired up to an endpoint (see docs/ROADMAP-v3.md
	// and CODE-REVIEW-FINDINGS.md O20). Kept on the interface anyway: it is
	// the only way the HistoryRepoSuite test suite can verify raw
	// interval-splitting behaviour (one row per interval, unmerged) rather
	// than behaviour that happens to look right through ServicesAt's grace
	// window. Removing it would weaken that coverage for no correctness gain.
	ServicesBetween(ctx context.Context, from, to time.Time) ([]ServiceInterval, error)

	// ConnectionsBetween returns every connection interval overlapping
	// [from, to], one row per interval. See ServicesBetween.
	ConnectionsBetween(ctx context.Context, from, to time.Time) ([]ConnectionInterval, error)

	// DeleteStale removes intervals that ended before the cutoff. Unlike
	// pruning current state, this is the retention boundary for history.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)

	// Bounds returns the earliest first_seen and latest last_seen across all
	// recorded intervals. ok is false when no history has been recorded yet,
	// distinguishing "empty" from a zero-value time range.
	Bounds(ctx context.Context) (bounds HistoryBounds, ok bool, err error)

	// StaleServices returns the most recent interval for every service whose
	// last observation is older than `before` - decommission candidates:
	// things that used to exist and haven't been seen since. One row per
	// service, its newest interval, not every interval it ever had.
	//
	// limit caps the rows returned, oldest first; a non-positive limit means
	// no cap. It is applied in the query rather than by the caller so a large
	// cluster's full candidate set is never materialized just to be discarded.
	StaleServices(ctx context.Context, before time.Time, limit int) ([]ServiceInterval, error)
}

// mergeServiceIntervals collapses intervals belonging to the same service into
// one entry spanning the union of their ranges.
//
// A service should have at most one interval covering any instant, but two
// concurrent observations of a first-ever appearance can both find nothing to
// extend and each open one. Rather than serialize every write to prevent a
// harmless and rare duplicate, reads tolerate it - which also means historical
// data written by an older or buggier version still reads back sensibly.
func mergeServiceIntervals(rows []ServiceInterval) []ServiceInterval {
	byID := make(map[string]*ServiceInterval, len(rows))
	order := make([]string, 0, len(rows))

	for i := range rows {
		row := rows[i]
		existing, ok := byID[row.ServiceID]
		if !ok {
			copied := row
			byID[row.ServiceID] = &copied
			order = append(order, row.ServiceID)
			continue
		}
		if row.FirstSeen.Before(existing.FirstSeen) {
			existing.FirstSeen = row.FirstSeen
		}
		if row.LastSeen.After(existing.LastSeen) {
			existing.LastSeen = row.LastSeen
		}
	}

	merged := make([]ServiceInterval, 0, len(order))
	for _, id := range order {
		merged = append(merged, *byID[id])
	}
	return merged
}

// mergeConnectionIntervals is mergeServiceIntervals for edges.
func mergeConnectionIntervals(rows []ConnectionInterval) []ConnectionInterval {
	byID := make(map[string]*ConnectionInterval, len(rows))
	order := make([]string, 0, len(rows))

	for i := range rows {
		row := rows[i]
		existing, ok := byID[row.ConnectionID]
		if !ok {
			copied := row
			byID[row.ConnectionID] = &copied
			order = append(order, row.ConnectionID)
			continue
		}
		if row.FirstSeen.Before(existing.FirstSeen) {
			existing.FirstSeen = row.FirstSeen
		}
		if row.LastSeen.After(existing.LastSeen) {
			existing.LastSeen = row.LastSeen
		}
	}

	merged := make([]ConnectionInterval, 0, len(order))
	for _, id := range order {
		merged = append(merged, *byID[id])
	}
	return merged
}

// MergeServiceIntervals is exported for storage backends to reuse.
func MergeServiceIntervals(rows []ServiceInterval) []ServiceInterval {
	return mergeServiceIntervals(rows)
}

// MergeConnectionIntervals is exported for storage backends to reuse.
func MergeConnectionIntervals(rows []ConnectionInterval) []ConnectionInterval {
	return mergeConnectionIntervals(rows)
}
