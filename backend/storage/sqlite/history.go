package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// HistoryRepo implements storage.HistoryRepository using SQLite.
type HistoryRepo struct {
	db *sql.DB
}

func (r *HistoryRepo) executor(ctx context.Context) interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
} {
	if tx := getTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

// RecordService extends the newest interval for this service if it is still
// within maxGap of `at`, otherwise opens a new one.
//
// The UPDATE targets exactly one row - the newest interval for this service -
// and only fires if that interval is recent enough to be considered still
// open. Zero rows affected means either the service has never been seen, its
// last sighting is older than maxGap, or `at` is behind that interval's
// last_seen (see below); all three cases open a new interval.
//
// The `last_seen <= ?` guard makes last_seen monotonically non-decreasing:
// events are timestamped independently per request with no per-service
// ordering guarantee, so a concurrent or replayed observation can arrive with
// an `at` behind one already recorded. Without the guard the SET would
// silently regress last_seen backward, corrupting the interval (potentially
// below first_seen) and making DeleteStale prune it earlier than its real
// retention window. Falling through to INSERT for that case can open a
// redundant interval nested inside the existing one, but the point-in-time
// reads merge duplicate intervals for the same entity (see
// MergeServiceIntervals), so that's harmless where regressing last_seen is not.
func (r *HistoryRepo) RecordService(ctx context.Context, svc *storage.Service, at time.Time, maxGap time.Duration) error {
	if svc == nil || svc.ID == "" {
		return fmt.Errorf("recording service interval: service is nil or has no ID")
	}

	exec := r.executor(ctx)
	cutoff := at.Add(-maxGap)

	res, err := exec.ExecContext(ctx, `
		UPDATE service_intervals
		SET last_seen = ?, name = ?, type = ?, tech = ?, namespace = ?, node = ?
		WHERE id = (
			SELECT id FROM service_intervals
			WHERE service_id = ?
			ORDER BY last_seen DESC
			LIMIT 1
		)
		AND last_seen >= ?
		AND last_seen <= ?`,
		at, svc.Name, svc.Type, svc.Tech, svc.Namespace, svc.Node,
		svc.ID, cutoff, at)
	if err != nil {
		return fmt.Errorf("extending service interval: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking extended service interval: %w", err)
	}
	if affected > 0 {
		return nil
	}

	// Guarded so an out-of-order observation that lands *inside* an existing
	// interval is a no-op instead of opening a redundant nested one. Under
	// concurrent ingestion the guard above fails constantly - goroutines stamp
	// time.Now() and then race to commit, so commit order barely relates to
	// timestamp order - and without this a single continuously observed
	// service accumulated one interval per racing write rather than one total.
	// That would make row count scale with event volume instead of with how
	// often the architecture actually changes, which is the premise the whole
	// interval model rests on.
	_, err = exec.ExecContext(ctx, `
		INSERT INTO service_intervals
			(service_id, name, type, tech, namespace, node, first_seen, last_seen)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM service_intervals
			WHERE service_id = ? AND first_seen <= ? AND last_seen >= ?
		)`,
		svc.ID, svc.Name, svc.Type, svc.Tech, svc.Namespace, svc.Node, at, at,
		svc.ID, at, at)
	if err != nil {
		return fmt.Errorf("opening service interval: %w", err)
	}
	return nil
}

// RecordConnection is RecordService for edges - see RecordService for why the
// extend UPDATE also guards against `at` being behind the recorded last_seen.
func (r *HistoryRepo) RecordConnection(ctx context.Context, conn *storage.Connection, at time.Time, maxGap time.Duration) error {
	if conn == nil || conn.ID == "" {
		return fmt.Errorf("recording connection interval: connection is nil or has no ID")
	}

	exec := r.executor(ctx)
	cutoff := at.Add(-maxGap)

	protocol := conn.Protocol
	if protocol == "" {
		protocol = "tcp"
	}

	res, err := exec.ExecContext(ctx, `
		UPDATE connection_intervals
		SET last_seen = ?
		WHERE id = (
			SELECT id FROM connection_intervals
			WHERE connection_id = ?
			ORDER BY last_seen DESC
			LIMIT 1
		)
		AND last_seen >= ?
		AND last_seen <= ?`,
		at, conn.ID, cutoff, at)
	if err != nil {
		return fmt.Errorf("extending connection interval: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking extended connection interval: %w", err)
	}
	if affected > 0 {
		return nil
	}

	// See RecordService for why this insert is guarded.
	_, err = exec.ExecContext(ctx, `
		INSERT INTO connection_intervals
			(connection_id, source_id, target_id, port, protocol, first_seen, last_seen)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM connection_intervals
			WHERE connection_id = ? AND first_seen <= ? AND last_seen >= ?
		)`,
		conn.ID, conn.SourceID, conn.TargetID, conn.Port, protocol, at, at,
		conn.ID, at, at)
	if err != nil {
		return fmt.Errorf("opening connection interval: %w", err)
	}
	return nil
}

const serviceIntervalColumns = `id, service_id, name, type, tech, namespace, node, first_seen, last_seen`

func (r *HistoryRepo) scanServiceIntervals(rows *sql.Rows) ([]storage.ServiceInterval, error) {
	defer rows.Close()

	var out []storage.ServiceInterval
	for rows.Next() {
		var si storage.ServiceInterval
		if err := rows.Scan(&si.ID, &si.ServiceID, &si.Name, &si.Type, &si.Tech,
			&si.Namespace, &si.Node, &si.FirstSeen, &si.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// ServicesAt returns services whose interval covers the given instant.
func (r *HistoryRepo) ServicesAt(ctx context.Context, at time.Time, grace time.Duration) ([]storage.ServiceInterval, error) {
	covers := at.Add(-grace)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+serviceIntervalColumns+`
		FROM service_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY service_id, first_seen`, at, covers)
	if err != nil {
		return nil, fmt.Errorf("querying services at %s: %w", at, err)
	}

	intervals, err := r.scanServiceIntervals(rows)
	if err != nil {
		return nil, err
	}
	return storage.MergeServiceIntervals(intervals), nil
}

// ServicesBetween returns service intervals overlapping the window.
func (r *HistoryRepo) ServicesBetween(ctx context.Context, from, to time.Time) ([]storage.ServiceInterval, error) {
	// Overlap, not containment: an interval that started before the window and
	// is still running belongs in the answer.
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+serviceIntervalColumns+`
		FROM service_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY service_id, first_seen`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying services between: %w", err)
	}

	return r.scanServiceIntervals(rows)
}

const connectionIntervalColumns = `id, connection_id, source_id, target_id, port, protocol, first_seen, last_seen`

func (r *HistoryRepo) scanConnectionIntervals(rows *sql.Rows) ([]storage.ConnectionInterval, error) {
	defer rows.Close()

	var out []storage.ConnectionInterval
	for rows.Next() {
		var ci storage.ConnectionInterval
		if err := rows.Scan(&ci.ID, &ci.ConnectionID, &ci.SourceID, &ci.TargetID,
			&ci.Port, &ci.Protocol, &ci.FirstSeen, &ci.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, rows.Err()
}

// ConnectionsAt returns edges whose interval covers the given instant.
func (r *HistoryRepo) ConnectionsAt(ctx context.Context, at time.Time, grace time.Duration) ([]storage.ConnectionInterval, error) {
	covers := at.Add(-grace)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY connection_id, first_seen`, at, covers)
	if err != nil {
		return nil, fmt.Errorf("querying connections at %s: %w", at, err)
	}

	intervals, err := r.scanConnectionIntervals(rows)
	if err != nil {
		return nil, err
	}
	return storage.MergeConnectionIntervals(intervals), nil
}

// ConnectionsBetween returns connection intervals overlapping the window.
func (r *HistoryRepo) ConnectionsBetween(ctx context.Context, from, to time.Time) ([]storage.ConnectionInterval, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY connection_id, first_seen`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying connections between: %w", err)
	}

	return r.scanConnectionIntervals(rows)
}

// Bounds returns the earliest and latest instants covered by recorded
// history, across both services and connections.
//
// Implemented with ORDER BY/LIMIT rather than MIN()/MAX(): the sqlite driver
// only recognises a column's declared DATETIME type on a plain column scan,
// so wrapping first_seen/last_seen in an aggregate loses that and returns a
// driver-internal string (including a monotonic clock reading) instead of a
// time.Time, which fails to scan.
func (r *HistoryRepo) Bounds(ctx context.Context) (storage.HistoryBounds, bool, error) {
	var earliest time.Time
	err := r.executor(ctx).QueryRowContext(ctx, `
		SELECT first_seen FROM (
			SELECT first_seen FROM service_intervals
			UNION ALL
			SELECT first_seen FROM connection_intervals
		)
		ORDER BY first_seen ASC LIMIT 1`).Scan(&earliest)
	if err == sql.ErrNoRows {
		return storage.HistoryBounds{}, false, nil
	}
	if err != nil {
		return storage.HistoryBounds{}, false, fmt.Errorf("querying earliest history bound: %w", err)
	}

	var latest time.Time
	err = r.executor(ctx).QueryRowContext(ctx, `
		SELECT last_seen FROM (
			SELECT last_seen FROM service_intervals
			UNION ALL
			SELECT last_seen FROM connection_intervals
		)
		ORDER BY last_seen DESC LIMIT 1`).Scan(&latest)
	// Same ErrNoRows handling as the first query, not just defensiveness: the
	// two run as separate statements, so the retention prune can empty the
	// tables in between. Treating that as an error would turn an ordinary
	// "no history" into a 500, and would diverge from the Postgres
	// implementation, which gets both bounds from one query and reports empty.
	if err == sql.ErrNoRows {
		return storage.HistoryBounds{}, false, nil
	}
	if err != nil {
		return storage.HistoryBounds{}, false, fmt.Errorf("querying latest history bound: %w", err)
	}

	return storage.HistoryBounds{Earliest: earliest, Latest: latest}, true, nil
}

// StaleServices returns each service's newest interval, for services whose
// newest interval ended before `before`.
//
// The correlated subquery keeps last_seen in the SELECT list as a plain
// column scan rather than an aggregate expression - MAX(last_seen) is only
// ever used inside the subquery, for comparison, never projected. That's the
// same decltype trap Bounds() hit: aggregating a DATETIME column in the
// outer SELECT returns an unparseable driver string instead of time.Time.
func (r *HistoryRepo) StaleServices(ctx context.Context, before time.Time, limit int) ([]storage.ServiceInterval, error) {
	query := `
		SELECT ` + serviceIntervalColumns + `
		FROM service_intervals si
		WHERE last_seen = (
			SELECT MAX(last_seen) FROM service_intervals WHERE service_id = si.service_id
		)
		AND last_seen < ?
		ORDER BY last_seen ASC`
	args := []interface{}{before}
	if limit > 0 {
		// Ordered oldest-first above, so truncating here keeps the strongest
		// candidates rather than an arbitrary slice of them.
		query += `
		LIMIT ?`
		args = append(args, limit)
	}

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying stale services: %w", err)
	}
	return r.scanServiceIntervals(rows)
}

// DeleteStale drops intervals that ended before the cutoff.
func (r *HistoryRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	exec := r.executor(ctx)

	svcRes, err := exec.ExecContext(ctx,
		`DELETE FROM service_intervals WHERE last_seen < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("pruning service intervals: %w", err)
	}
	svcDeleted, err := svcRes.RowsAffected()
	if err != nil {
		return 0, err
	}

	connRes, err := exec.ExecContext(ctx,
		`DELETE FROM connection_intervals WHERE last_seen < ?`, before)
	if err != nil {
		return svcDeleted, fmt.Errorf("pruning connection intervals: %w", err)
	}
	connDeleted, err := connRes.RowsAffected()
	if err != nil {
		return svcDeleted, err
	}

	return svcDeleted + connDeleted, nil
}
