package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// DriftRepo implements storage.DriftRepository using PostgreSQL.
//
// Same shape as the SQLite implementation (backend/storage/sqlite/drift.go);
// the differences are $N placeholders, RETURNING instead of LastInsertId, and
// that timestamptz makes the UTC normalization there merely tidy rather than
// load-bearing.
type DriftRepo struct {
	db *sql.DB
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func (r *DriftRepo) executor(ctx context.Context) sqlExecutor {
	if tx := getTx(ctx); tx != nil {
		return tx
	}
	return r.db
}

const driftEventColumns = `id, detected_at, kind, connection_id, source_id, source_name,
	target_id, target_name, port, protocol, external, first_seen, last_seen, fingerprint`

// InsertEvents inserts events in one transaction, skipping duplicates by
// fingerprint.
func (r *DriftRepo) InsertEvents(ctx context.Context, events []storage.DriftEvent) ([]storage.DriftEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}

	var inserted []storage.DriftEvent
	insert := func(exec sqlExecutor) error {
		inserted = inserted[:0]
		for _, ev := range events {
			if ev.Fingerprint == "" {
				return fmt.Errorf("inserting drift event for %s: no fingerprint", ev.ConnectionID)
			}
			// Truncated here rather than left to Postgres, which would round:
			// the returned event must carry exactly the stored values.
			ev = storage.NormalizeDriftEvent(ev)
			// ON CONFLICT DO NOTHING RETURNING yields no row for a duplicate,
			// which is how "already recorded" is told apart from "inserted".
			var id int64
			err := exec.QueryRowContext(ctx, `
				INSERT INTO drift_events
					(fingerprint, detected_at, kind, connection_id, source_id, source_name,
					 target_id, target_name, port, protocol, external, first_seen, last_seen)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
				ON CONFLICT (fingerprint) DO NOTHING
				RETURNING id`,
				ev.Fingerprint, ev.DetectedAt, string(ev.Kind), ev.ConnectionID,
				ev.SourceID, ev.SourceName, ev.TargetID, ev.TargetName, int(ev.Port), ev.Protocol,
				ev.External, ev.FirstSeen, ev.LastSeen).Scan(&id)
			if err == sql.ErrNoRows {
				continue // already recorded
			}
			if err != nil {
				return fmt.Errorf("inserting drift event: %w", err)
			}
			ev.ID = id
			inserted = append(inserted, ev)
		}
		return nil
	}

	if tx := getTx(ctx); tx != nil {
		if err := insert(tx); err != nil {
			return nil, err
		}
		return inserted, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning drift event transaction: %w", err)
	}
	if err := insert(tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing drift events: %w", err)
	}
	return inserted, nil
}

// driftWhere builds the WHERE clause shared by ListEvents and Summarize.
// It returns the clause, its arguments, and the next free placeholder number.
func driftWhere(f storage.DriftEventFilter) (string, []interface{}, int) {
	where := " WHERE 1=1"
	var args []interface{}
	n := 1
	arg := func(v interface{}) string {
		args = append(args, v)
		p := fmt.Sprintf("$%d", n)
		n++
		return p
	}
	if !f.Since.IsZero() {
		where += " AND detected_at >= " + arg(storage.DriftTime(f.Since))
	}
	if !f.After.IsZero() {
		// Exclusive in the query itself: an offset added to the bound
		// instead would vanish into the stored microsecond precision.
		where += " AND detected_at > " + arg(storage.DriftTime(f.After))
	}
	if !f.Until.IsZero() {
		where += " AND detected_at < " + arg(storage.DriftTime(f.Until))
	}
	if f.Kind != "" {
		where += " AND kind = " + arg(string(f.Kind))
	}
	if f.External != nil {
		where += " AND external = " + arg(*f.External)
	}
	if f.Before != nil {
		at := storage.DriftTime(f.Before.DetectedAt)
		where += " AND (detected_at < " + arg(at) +
			" OR (detected_at = " + arg(at) + " AND id < " + arg(f.Before.ID) + "))"
	}
	return where, args, n
}

// ListEvents returns matching events, newest first.
func (r *DriftRepo) ListEvents(ctx context.Context, filter storage.DriftEventFilter) ([]storage.DriftEvent, error) {
	where, args, next := driftWhere(filter)
	query := `SELECT ` + driftEventColumns + ` FROM drift_events` + where +
		` ORDER BY detected_at DESC, id DESC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(` LIMIT $%d`, next)
		args = append(args, filter.Limit)
	}

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying drift events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []storage.DriftEvent
	for rows.Next() {
		var ev storage.DriftEvent
		var kind string
		var port int
		if err := rows.Scan(&ev.ID, &ev.DetectedAt, &kind, &ev.ConnectionID, &ev.SourceID,
			&ev.SourceName, &ev.TargetID, &ev.TargetName, &port, &ev.Protocol,
			&ev.External, &ev.FirstSeen, &ev.LastSeen, &ev.Fingerprint); err != nil {
			return nil, fmt.Errorf("scanning drift event: %w", err)
		}
		ev.Kind = storage.DriftEventKind(kind)
		ev.Port = uint16(port)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Summarize counts events in the filter's time window.
func (r *DriftRepo) Summarize(ctx context.Context, filter storage.DriftEventFilter) (storage.DriftSummary, error) {
	where, args, _ := driftWhere(storage.DriftEventFilter{Since: filter.Since, After: filter.After, Until: filter.Until})
	exec := r.executor(ctx)

	var sum storage.DriftSummary
	rows, err := exec.QueryContext(ctx,
		`SELECT kind, external, COUNT(*) FROM drift_events`+where+` GROUP BY kind, external`, args...)
	if err != nil {
		return sum, fmt.Errorf("counting drift events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var external bool
		var n int64
		if err := rows.Scan(&kind, &external, &n); err != nil {
			return sum, fmt.Errorf("scanning drift event counts: %w", err)
		}
		sum.Total += n
		switch storage.DriftEventKind(kind) {
		case storage.DriftAppeared:
			sum.Appeared += n
		case storage.DriftDisappeared:
			sum.Disappeared += n
		}
		if external {
			sum.External += n
		}
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	if sum.Total == 0 {
		return sum, nil
	}

	var latest time.Time
	err = exec.QueryRowContext(ctx,
		`SELECT detected_at FROM drift_events`+where+` ORDER BY detected_at DESC LIMIT 1`, args...).Scan(&latest)
	if err == sql.ErrNoRows {
		return sum, nil // pruned between the two statements
	}
	if err != nil {
		return sum, fmt.Errorf("querying latest drift event: %w", err)
	}
	sum.Latest = &latest
	return sum, nil
}

// DeleteBefore drops events detected before the cutoff.
func (r *DriftRepo) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.executor(ctx).ExecContext(ctx,
		`DELETE FROM drift_events WHERE detected_at < $1`, storage.DriftTime(before))
	if err != nil {
		return 0, fmt.Errorf("pruning drift events: %w", err)
	}
	return res.RowsAffected()
}

// Checkpoint returns the last recorded detection pass.
func (r *DriftRepo) Checkpoint(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT checked_at FROM drift_checkpoint WHERE id = 1`).Scan(&at)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading drift checkpoint: %w", err)
	}
	return at, true, nil
}

// SetCheckpoint records a detection pass, never moving it backwards.
func (r *DriftRepo) SetCheckpoint(ctx context.Context, at time.Time) error {
	_, err := r.executor(ctx).ExecContext(ctx, `
		INSERT INTO drift_checkpoint (id, checked_at) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET checked_at = EXCLUDED.checked_at
		WHERE EXCLUDED.checked_at > drift_checkpoint.checked_at`, storage.DriftTime(at))
	if err != nil {
		return fmt.Errorf("writing drift checkpoint: %w", err)
	}
	return nil
}

// ResetCheckpoint sets the checkpoint unconditionally.
func (r *DriftRepo) ResetCheckpoint(ctx context.Context, at time.Time) error {
	_, err := r.executor(ctx).ExecContext(ctx, `
		INSERT INTO drift_checkpoint (id, checked_at) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET checked_at = EXCLUDED.checked_at`, storage.DriftTime(at))
	if err != nil {
		return fmt.Errorf("resetting drift checkpoint: %w", err)
	}
	return nil
}
