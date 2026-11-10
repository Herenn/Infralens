package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

var _ storage.LatestConnectionIntervaler = (*HistoryRepo)(nil)

// LatestConnectionIntervals returns the newest interval of each connection
// overlapping [from, to].
//
// The inner GROUP BY relies on SQLite's documented bare-column behaviour: with
// MAX(), the other selected columns come from the row holding the maximum, so
// it yields that row's id while scanning only idx_connection_intervals_lookup
// (connection_id, last_seen). The outer query then reads plain columns, which
// matters: the driver only returns DATETIME columns as time.Time on a plain
// column scan (see Bounds), not through an aggregate.
func (r *HistoryRepo) LatestConnectionIntervals(ctx context.Context, from, to time.Time) ([]storage.ConnectionInterval, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE id IN (
			SELECT id FROM (
				SELECT id, MAX(last_seen) FROM connection_intervals GROUP BY connection_id
			)
		)
		AND first_seen <= ? AND last_seen >= ?
		ORDER BY connection_id`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying latest connection intervals: %w", err)
	}
	return r.scanConnectionIntervals(rows)
}
