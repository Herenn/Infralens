package storage

import (
	"context"
	"time"
)

// LatestConnectionIntervaler is an optional HistoryRepository extension: the
// newest interval of every connection that overlaps [from, to], one row per
// connection, aggregated in the database.
//
// Optional (asserted by callers, with a ConnectionsBetween fallback) rather
// than added to HistoryRepository, so other implementations of that
// interface keep compiling.
type LatestConnectionIntervaler interface {
	LatestConnectionIntervals(ctx context.Context, from, to time.Time) ([]ConnectionInterval, error)
}
