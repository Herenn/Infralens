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
	ServicesAt(ctx context.Context, at time.Time) ([]ServiceInterval, error)

	// ConnectionsAt returns the edges present at a point in time.
	ConnectionsAt(ctx context.Context, at time.Time) ([]ConnectionInterval, error)

	// ServicesBetween returns service intervals overlapping [from, to].
	ServicesBetween(ctx context.Context, from, to time.Time) ([]ServiceInterval, error)

	// ConnectionsBetween returns connection intervals overlapping [from, to].
	ConnectionsBetween(ctx context.Context, from, to time.Time) ([]ConnectionInterval, error)

	// DeleteStale removes intervals that ended before the cutoff. Unlike
	// pruning current state, this is the retention boundary for history.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)
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
