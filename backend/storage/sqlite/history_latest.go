package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// LatestConnectionIntervals returns, for each connection with an interval
// overlapping [from, to], the newest such interval (as the Postgres store's
// DISTINCT ON does: the window filters before the newest is picked).
//
// The inner GROUP BY relies on SQLite's documented bare-column behaviour: with
// MAX(), the other selected columns come from the row holding the maximum, so
// it yields that row's id while scanning only idx_connection_intervals_lookup
// (connection_id, last_seen). The outer query then reads plain columns, which
// matters: the driver only returns DATETIME columns as time.Time on a plain
// column scan (see Bounds), not through an aggregate.
func (r *HistoryRepo) LatestConnectionIntervals(ctx context.Context, from, to time.Time) ([]storage.ConnectionInterval, error) {
	from, to = queryTime(from), queryTime(to)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE id IN (
			SELECT id FROM (
				SELECT id, MAX(last_seen) FROM connection_intervals
				WHERE first_seen <= ? AND last_seen >= ?
				GROUP BY connection_id
			)
		)
		ORDER BY connection_id`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying latest connection intervals: %w", err)
	}
	return r.scanConnectionIntervals(rows)
}
