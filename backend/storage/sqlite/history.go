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
// open. Zero rows affected means either the service has never been seen or its
// last sighting is older than maxGap; both cases are a new interval.
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
		AND last_seen >= ?`,
		at, svc.Name, svc.Type, svc.Tech, svc.Namespace, svc.Node,
		svc.ID, cutoff)
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

	_, err = exec.ExecContext(ctx, `
		INSERT INTO service_intervals
			(service_id, name, type, tech, namespace, node, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		svc.ID, svc.Name, svc.Type, svc.Tech, svc.Namespace, svc.Node, at, at)
	if err != nil {
		return fmt.Errorf("opening service interval: %w", err)
	}
	return nil
}

// RecordConnection is RecordService for edges.
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
		AND last_seen >= ?`,
		at, conn.ID, cutoff)
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

	_, err = exec.ExecContext(ctx, `
		INSERT INTO connection_intervals
			(connection_id, source_id, target_id, port, protocol, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		conn.ID, conn.SourceID, conn.TargetID, conn.Port, protocol, at, at)
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
func (r *HistoryRepo) ServicesAt(ctx context.Context, at time.Time) ([]storage.ServiceInterval, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+serviceIntervalColumns+`
		FROM service_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY service_id, first_seen`, at, at)
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

	intervals, err := r.scanServiceIntervals(rows)
	if err != nil {
		return nil, err
	}
	return storage.MergeServiceIntervals(intervals), nil
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
func (r *HistoryRepo) ConnectionsAt(ctx context.Context, at time.Time) ([]storage.ConnectionInterval, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT `+connectionIntervalColumns+`
		FROM connection_intervals
		WHERE first_seen <= ? AND last_seen >= ?
		ORDER BY connection_id, first_seen`, at, at)
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

	intervals, err := r.scanConnectionIntervals(rows)
	if err != nil {
		return nil, err
	}
	return storage.MergeConnectionIntervals(intervals), nil
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
