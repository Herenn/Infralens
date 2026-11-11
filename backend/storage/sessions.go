package storage

import (
	"context"
	"time"
)

// SessionRevocation records a UI session ended early by logout.
type SessionRevocation struct {
	SessionID string
	// ExpiresAt is when the session would have expired anyway; the record is
	// useless after it.
	ExpiresAt time.Time
	// CreatedAt is assigned by the database, not the caller, so replicas can
	// sync incrementally on it without trusting each other's clocks.
	CreatedAt time.Time
}

// SessionRevocationRepository persists UI session revocations. Times have
// one-second resolution.
type SessionRevocationRepository interface {
	// Revoke records that sessionID was logged out. Revoking the same ID
	// again is a no-op.
	Revoke(ctx context.Context, sessionID string, expiresAt time.Time) error

	// List returns revocations created at or after createdSince that have
	// not yet expired at activeAt.
	List(ctx context.Context, createdSince, activeAt time.Time) ([]SessionRevocation, error)

	// DeleteExpired removes revocations whose session expired before now.
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// SessionRevocationStore is implemented by stores that can persist UI session
// revocations (SQLite and Postgres both do). It is a separate, optional
// interface rather than part of Store so that other Store implementations -
// test fakes included - need not change.
type SessionRevocationStore interface {
	SessionRevocations() SessionRevocationRepository
}
