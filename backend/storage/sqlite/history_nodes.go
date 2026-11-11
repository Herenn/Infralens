package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// RecordNode extends or opens the reporting interval of a node's agent. Same
// extend-or-open rules as RecordService, keyed by (group, node): two
// clusters may have nodes with the same name.
func (r *HistoryRepo) RecordNode(ctx context.Context, node, group string, at time.Time, maxGap time.Duration) error {
	if node == "" {
		return fmt.Errorf("recording node interval: node has no name")
	}
	exec := r.executor(ctx)
	at = dbTime(at)
	cutoff := at.Add(-maxGap)

	res, err := exec.ExecContext(ctx, `
		UPDATE node_intervals
		SET last_seen = ?
		WHERE id = (
			SELECT id FROM node_intervals
			WHERE group_name = ? AND node_name = ?
			ORDER BY last_seen DESC
			LIMIT 1
		)
		AND last_seen >= ?
		AND last_seen <= ?`,
		at, group, node, cutoff, at)
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
		SELECT ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM node_intervals
			WHERE group_name = ? AND node_name = ? AND first_seen <= ? AND last_seen >= ?
		)`,
		node, group, at, at, group, node, at, at)
	if err != nil {
		return fmt.Errorf("opening node interval: %w", err)
	}
	return nil
}

// NodesBetween returns node reporting intervals overlapping [from, to].
func (r *HistoryRepo) NodesBetween(ctx context.Context, from, to time.Time) ([]storage.NodeInterval, error) {
	from, to = queryTime(from), queryTime(to)
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT id, node_name, group_name, first_seen, last_seen
		FROM node_intervals
		WHERE first_seen <= ? AND last_seen >= ?
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
