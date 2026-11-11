package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// RecordNode extends or opens the reporting interval of a node's agent. Same
// extend-or-open rules as RecordService, keyed by node name. Mirrors the
// SQLite implementation.
func (r *HistoryRepo) RecordNode(ctx context.Context, node, group string, at time.Time, maxGap time.Duration) error {
	if node == "" {
		return fmt.Errorf("recording node interval: node has no name")
	}
	exec := r.executor(ctx)
	at = pgTime(at)
	cutoff := at.Add(-maxGap)

	res, err := exec.ExecContext(ctx, `
		UPDATE node_intervals
		SET last_seen = $1, group_name = $5
		WHERE id = (
			SELECT id FROM node_intervals
			WHERE node_name = $2
			ORDER BY last_seen DESC
			LIMIT 1
		)
		AND last_seen >= $3
		AND last_seen <= $4`,
		at, node, cutoff, at, group)
	if err != nil {
		return fmt.Errorf("extending node interval: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("checking extended node interval: %w", err)
	} else if n > 0 {
		return nil
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO node_intervals (node_name, group_name, first_seen, last_seen)
		SELECT $1::text, $3::text, $2::timestamptz, $2::timestamptz
		WHERE NOT EXISTS (
			SELECT 1 FROM node_intervals
			WHERE node_name = $1 AND first_seen <= $2 AND last_seen >= $2
		)`,
		node, at, group)
	if err != nil {
		return fmt.Errorf("opening node interval: %w", err)
	}
	return nil
}

// NodesBetween returns node reporting intervals overlapping [from, to].
func (r *HistoryRepo) NodesBetween(ctx context.Context, from, to time.Time) ([]storage.NodeInterval, error) {
	from, to = pgTime(from), pgTime(to)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT id, node_name, group_name, first_seen, last_seen
		FROM node_intervals
		WHERE first_seen <= $1 AND last_seen >= $2
		ORDER BY first_seen, id`, to, from)
	if err != nil {
		return nil, fmt.Errorf("querying node intervals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []storage.NodeInterval
	for rows.Next() {
		var ni storage.NodeInterval
		if err := rows.Scan(&ni.ID, &ni.Node, &ni.Group, &ni.FirstSeen, &ni.LastSeen); err != nil {
			return nil, err
		}
		ni.FirstSeen, ni.LastSeen = ni.FirstSeen.UTC(), ni.LastSeen.UTC()
		out = append(out, ni)
	}
	return out, rows.Err()
}
