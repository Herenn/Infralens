package storage

import (
	"context"
	"time"
)

// DriftEventKind is what happened to a dependency.
type DriftEventKind string

const (
	// DriftAppeared: a dependency never seen before was observed.
	DriftAppeared DriftEventKind = "appeared"

	// DriftDisappeared: an established dependency stopped being observed.
	DriftDisappeared DriftEventKind = "disappeared"
)

// Valid reports whether k is a known kind.
func (k DriftEventKind) Valid() bool {
	return k == DriftAppeared || k == DriftDisappeared
}

// DriftEvent is one architecture change: a single edge that appeared or
// disappeared, as detected by drift detection. It is the persisted form of
// what the drift webhook announces, one row per edge rather than one per
// report, so the feed can be filtered and paged.
type DriftEvent struct {
	ID         int64          `json:"id"`
	DetectedAt time.Time      `json:"detected_at"`
	Kind       DriftEventKind `json:"kind"`

	ConnectionID string `json:"connection_id"`
	SourceID     string `json:"source_id"`
	TargetID     string `json:"target_id"`

	// SourceName / TargetName are human-readable names for the endpoints,
	// captured when the event was detected. They are snapshots on purpose:
	// a disappeared dependency's endpoints have often been pruned from
	// current state by the time anyone reads the feed, and the IDs of
	// unresolved peers are bare IPs. Empty means "no better name than the
	// ID was known" - callers fall back to the ID.
	SourceName string `json:"source_name,omitempty"`
	TargetName string `json:"target_name,omitempty"`

	Port     uint16 `json:"port"`
	Protocol string `json:"protocol,omitempty"`

	// External is true when the target is a public internet address.
	External bool `json:"external"`

	// FirstSeen / LastSeen are the edge's observed span at detection time:
	// for an appeared edge, when it was first observed; for a disappeared
	// edge, when it was last observed.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Fingerprint identifies the underlying occurrence, independent of when
	// or by which process it was detected. Inserting an event whose
	// fingerprint already exists is a no-op - which is what keeps a restart
	// (or a second backend replica) that re-detects the same change from
	// writing it twice. See service.DriftDetector for how it is built.
	Fingerprint string `json:"-"`
}

// DriftEventCursor is a keyset position in the newest-first event order.
type DriftEventCursor struct {
	DetectedAt time.Time
	ID         int64
}

// DriftEventFilter selects drift events. Zero values mean "no constraint".
type DriftEventFilter struct {
	// Since includes only events detected at or after this instant.
	Since time.Time
	// After includes only events detected strictly after this instant - what
	// "anything newer than the newest event I have shown" needs. Stored
	// times have microsecond precision; a value read back from the store
	// matches exactly.
	After time.Time
	// Until includes only events detected strictly before this instant.
	Until time.Time
	// Kind restricts to one kind of change.
	Kind DriftEventKind
	// External restricts to internet-bound (true) or internal (false) edges.
	External *bool
	// Before continues a listing: only events strictly after this position
	// in newest-first order (i.e. older ones).
	Before *DriftEventCursor
	// Limit caps the rows returned; non-positive means no cap.
	Limit int
}

// DriftSummary counts the events in a window.
type DriftSummary struct {
	Total       int64 `json:"total"`
	Appeared    int64 `json:"appeared"`
	Disappeared int64 `json:"disappeared"`
	// External counts internet-bound events of either kind.
	External int64 `json:"external"`
	// Latest is when the newest counted event was detected, nil if none.
	Latest *time.Time `json:"latest,omitempty"`
}

// DriftRepository persists the architecture drift feed.
type DriftRepository interface {
	// InsertEvents stores events, skipping any whose Fingerprint is already
	// stored. It returns the events actually inserted, with IDs assigned, in
	// input order - so callers can notify about exactly what is new. Times
	// are stored, and returned, as UTC with microsecond precision, so what a
	// caller publishes is exactly what a later read returns.
	InsertEvents(ctx context.Context, events []DriftEvent) ([]DriftEvent, error)

	// ListEvents returns events matching the filter, newest first (by
	// detected_at, then ID).
	ListEvents(ctx context.Context, filter DriftEventFilter) ([]DriftEvent, error)

	// Summarize counts the events in the filter's time window (Since, After,
	// Until; other fields are ignored).
	Summarize(ctx context.Context, filter DriftEventFilter) (DriftSummary, error)

	// DeleteBefore removes events detected before the cutoff - the retention
	// boundary for the feed.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)

	// Checkpoint returns the time of the last completed detection pass, as
	// recorded by SetCheckpoint. ok is false if none has been recorded.
	Checkpoint(ctx context.Context) (at time.Time, ok bool, err error)

	// SetCheckpoint records a completed detection pass. It never moves the
	// checkpoint backwards, so concurrent detectors (several replicas) agree
	// on the newest one.
	SetCheckpoint(ctx context.Context, at time.Time) error

	// ResetCheckpoint sets the checkpoint unconditionally - for repairing one
	// left in the future by a clock that jumped forward and back.
	ResetCheckpoint(ctx context.Context, at time.Time) error
}

// DriftTime is the form drift event times are stored in by every backend:
// UTC, microsecond precision (what Postgres keeps).
func DriftTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// NormalizeDriftEvent puts an event's times in DriftTime form.
func NormalizeDriftEvent(ev DriftEvent) DriftEvent {
	ev.DetectedAt = DriftTime(ev.DetectedAt)
	ev.FirstSeen = DriftTime(ev.FirstSeen)
	ev.LastSeen = DriftTime(ev.LastSeen)
	if ev.Protocol == "" {
		ev.Protocol = "tcp"
	}
	return ev
}
