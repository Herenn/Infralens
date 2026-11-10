package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// LatestConnectionIntervals returns the newest interval of each connection
// overlapping [from, to]. DISTINCT ON walks idx_connection_intervals_lookup
// (connection_id, last_seen) instead of returning every interval.
func (r *HistoryRepo) LatestConnectionIntervals(ctx context.Context, from, to time.Time) ([]storage.ConnectionInterval, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT DISTINCT ON (connection_id) `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE first_seen <= $1 AND last_seen >= $2
		ORDER BY connection_id, last_seen DESC`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying latest connection intervals: %w", err)
	}
	return r.scanConnectionIntervals(rows)
}
