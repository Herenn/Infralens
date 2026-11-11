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
// One index lookup (idx_connection_intervals_lookup) per distinct edge in the
// window picks its newest interval, ties on last_seen going to the newest row
// (highest id) - the same answer as the Postgres DISTINCT ON query. The outer
// query reads plain columns, which matters: the driver only returns DATETIME
// columns as time.Time on a plain column scan (see Bounds), not through an
// aggregate.
func (r *HistoryRepo) LatestConnectionIntervals(ctx context.Context, from, to time.Time) ([]storage.ConnectionInterval, error) {
	from, to = queryTime(from), queryTime(to)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE id IN (
			SELECT (
				SELECT c2.id FROM connection_intervals c2
				WHERE c2.connection_id = d.connection_id
				AND c2.first_seen <= ? AND c2.last_seen >= ?
				ORDER BY c2.last_seen DESC, c2.id DESC
				LIMIT 1
			)
			FROM (
				SELECT DISTINCT connection_id FROM connection_intervals
				WHERE first_seen <= ? AND last_seen >= ?
			) d
		)
		ORDER BY connection_id`, to, from, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying latest connection intervals: %w", err)
	}
	return r.scanConnectionIntervals(rows)
}
