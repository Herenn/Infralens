package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// SessionRevocationRepo persists UI logouts (table ui_session_revocations).
type SessionRevocationRepo struct {
	db *sql.DB
}

var _ storage.SessionRevocationStore = (*Store)(nil)

// SessionRevocations returns the UI session revocation repository.
func (s *Store) SessionRevocations() storage.SessionRevocationRepository {
	return &SessionRevocationRepo{db: s.db}
}

// Revoke records a logout. Idempotent.
func (r *SessionRevocationRepo) Revoke(ctx context.Context, sessionID string, expiresAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO ui_session_revocations (session_id, expires_at) VALUES (?, ?)
		 ON CONFLICT(session_id) DO NOTHING`,
		sessionID, expiresAt.Unix())
	if err != nil {
		return fmt.Errorf("recording session revocation: %w", err)
	}
	return nil
}

// List returns unexpired revocations created at or after createdSince.
func (r *SessionRevocationRepo) List(ctx context.Context, createdSince, activeAt time.Time) ([]storage.SessionRevocation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT session_id, expires_at, created_at FROM ui_session_revocations
		 WHERE created_at >= ? AND expires_at > ? ORDER BY created_at`,
		createdSince.Unix(), activeAt.Unix())
	if err != nil {
		return nil, fmt.Errorf("listing session revocations: %w", err)
	}
	defer rows.Close()

	var out []storage.SessionRevocation
	for rows.Next() {
		var id string
		var exp, created int64
		if err := rows.Scan(&id, &exp, &created); err != nil {
			return nil, fmt.Errorf("scanning session revocation: %w", err)
		}
		out = append(out, storage.SessionRevocation{
			SessionID: id,
			ExpiresAt: time.Unix(exp, 0),
			CreatedAt: time.Unix(created, 0),
		})
	}
	return out, rows.Err()
}

// DeleteExpired removes revocations for sessions that have expired anyway.
func (r *SessionRevocationRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM ui_session_revocations WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("pruning session revocations: %w", err)
	}
	return res.RowsAffected()
}
