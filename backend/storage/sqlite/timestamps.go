package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// dbTime is the form every time takes on its way into this store, whether
// written to a row or compared against one: UTC, truncated to microseconds.
//
// This driver stores DATETIME as time.Time.String() text - zone offset
// included, plus a " m=+1.234" monotonic-clock suffix for values straight
// from time.Now() - and SQLite compares that text byte by byte. Comparisons
// are therefore only correct between values rendered identically. Up to
// v3.0 rows were written in the process's local zone, which broke whenever
// a row and a query differed in zone (the UI sends ?at= in UTC), across a
// DST change (the repeated hour sorts before the hour preceding it), when a
// database moved between hosts or the host's TZ changed, and at exact
// instants (a stored monotonic suffix sorts after the same instant without
// one). Rendering everything as UTC removes all four: one fixed offset, no
// DST, no monotonic reading (UTC() strips it).
//
// Microseconds because that is what Postgres keeps: both stores then return
// exactly the values they were given, and a time read back from one table
// compares equal to the same instant stored in another.
func dbTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// timestampColumns lists every DATETIME column, per table.
var timestampColumns = []struct {
	table   string
	columns []string
}{
	{"services", []string{"last_seen", "created_at", "updated_at"}},
	{"service_inspections", []string{"inspected_at"}},
	{"connections", []string{"last_seen", "created_at", "updated_at"}},
	{"node_metrics", []string{"last_seen", "updated_at"}},
	{"service_intervals", []string{"first_seen", "last_seen"}},
	{"connection_intervals", []string{"first_seen", "last_seen"}},
	{"drift_events", []string{"detected_at", "first_seen", "last_seen"}},
	{"drift_checkpoint", []string{"checked_at"}},
	{"node_intervals", []string{"first_seen", "last_seen"}},
}

// normalizeBatch is how many rows one normalisation transaction rewrites:
// large enough that a big database converts in seconds, small enough that
// each write transaction (and so the time anything else waits on the single
// connection) stays short.
const normalizeBatch = 2000

// NormalizeTimestamps rewrites every stored time that is not already in
// dbTime form - rows written by v3.0 and earlier in the host's local zone,
// with monotonic suffixes, or with nanoseconds - as UTC microseconds.
//
// Safe to run at any startup: rows already in canonical form are skipped by
// the query itself, each batch is its own transaction, and progress is
// keyed on rowid, so an interrupted run leaves every row either converted
// or untouched and the next run picks up the rest. Intervals the old
// behaviour split in two are left split; reads already tolerate that.
func (s *Store) NormalizeTimestamps(ctx context.Context) (int64, error) {
	started := time.Now()
	var total int64
	for _, tc := range timestampColumns {
		n, err := s.normalizeTable(ctx, tc.table, tc.columns)
		total += n
		if err != nil {
			return total, fmt.Errorf("normalizing %s timestamps: %w", tc.table, err)
		}
	}
	if total > 0 {
		log.WithFields(log.Fields{
			"rows": total,
			"took": time.Since(started).Round(time.Millisecond),
		}).Info("Normalized stored timestamps to UTC")
	}
	return total, nil
}

// needsNormalizing is a SQL predicate true when col holds a time not in
// dbTime's text form: not rendered as UTC (a local zone, a monotonic suffix,
// or SQLite's own CURRENT_TIMESTAMP format), or more than six fractional
// digits.
func needsNormalizing(col string) string {
	return fmt.Sprintf(`(%[1]s IS NOT NULL AND (CAST(%[1]s AS TEXT) NOT LIKE '%% +0000 UTC'
		OR CAST(%[1]s AS TEXT) GLOB '*.[0-9][0-9][0-9][0-9][0-9][0-9][0-9]*'))`, col)
}

func (s *Store) normalizeTable(ctx context.Context, table string, columns []string) (int64, error) {
	preds := make([]string, len(columns))
	for i, c := range columns {
		preds[i] = needsNormalizing(c)
	}
	query := fmt.Sprintf(`SELECT rowid, %s FROM %s WHERE rowid > ? AND (%s) ORDER BY rowid LIMIT %d`,
		strings.Join(columns, ", "), table, strings.Join(preds, " OR "), normalizeBatch)
	sets := make([]string, len(columns))
	for i, c := range columns {
		sets[i] = c + " = ?"
	}
	update := fmt.Sprintf(`UPDATE %s SET %s WHERE rowid = ?`, table, strings.Join(sets, ", "))

	var total int64
	var after int64
	for {
		type row struct {
			rowid  int64
			values []interface{}
		}
		rows, err := s.db.QueryContext(ctx, query, after)
		if err != nil {
			return total, err
		}
		var batch []row
		for rows.Next() {
			r := row{values: make([]interface{}, len(columns))}
			dest := []interface{}{&r.rowid}
			for i := range r.values {
				dest = append(dest, &r.values[i])
			}
			if err := rows.Scan(dest...); err != nil {
				_ = rows.Close()
				return total, err
			}
			batch = append(batch, r)
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(batch) == 0 {
			return total, nil
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return total, err
		}
		stmt, err := tx.PrepareContext(ctx, update)
		if err != nil {
			_ = tx.Rollback()
			return total, err
		}
		for _, r := range batch {
			args := make([]interface{}, 0, len(columns)+1)
			for _, v := range r.values {
				args = append(args, normalizedValue(v))
			}
			args = append(args, r.rowid)
			if _, err := stmt.ExecContext(ctx, args...); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return total, err
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			return total, err
		}
		total += int64(len(batch))
		after = batch[len(batch)-1].rowid
		if len(batch) < normalizeBatch {
			return total, nil
		}
	}
}

// normalizedValue converts one scanned DATETIME value. The driver parses the
// text it wrote (zone and monotonic suffix included) into a time.Time; any
// value it could not parse is written back unchanged rather than guessed at.
func normalizedValue(v interface{}) interface{} {
	switch t := v.(type) {
	case time.Time:
		return dbTime(t)
	case nil:
		return nil
	default:
		return v
	}
}
