package storage

import (
	"context"
	"testing"
	"time"
)

// SessionRevocationSuite exercises a SessionRevocationRepository. Session IDs
// are prefixed "suite-rev-" so a shared database can be cleaned up after.
func SessionRevocationSuite(t *testing.T, repo SessionRevocationRepository) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	if err := repo.Revoke(ctx, "suite-rev-active", now.Add(time.Hour)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := repo.Revoke(ctx, "suite-rev-active", now.Add(time.Hour)); err != nil {
		t.Fatalf("revoking the same session twice must be a no-op, got %v", err)
	}
	if err := repo.Revoke(ctx, "suite-rev-expired", now.Add(-time.Minute)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	got, err := repo.List(ctx, time.Unix(0, 0), now)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := map[string]SessionRevocation{}
	for _, r := range got {
		found[r.SessionID] = r
	}
	active, ok := found["suite-rev-active"]
	if !ok {
		t.Fatalf("active revocation not listed: %+v", got)
	}
	if !active.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want %v", active.ExpiresAt, now.Add(time.Hour))
	}
	// created_at comes from the database clock; it must be roughly now.
	if d := active.CreatedAt.Sub(now); d < -time.Minute || d > time.Minute {
		t.Errorf("CreatedAt = %v, want close to %v", active.CreatedAt, now)
	}
	if _, ok := found["suite-rev-expired"]; ok {
		t.Error("expired revocation listed as active")
	}

	// Incremental sync: nothing created after the active row's created_at+1h.
	later, err := repo.List(ctx, active.CreatedAt.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("List since: %v", err)
	}
	for _, r := range later {
		if r.SessionID == "suite-rev-active" {
			t.Error("List(createdSince) returned a row created before createdSince")
		}
	}

	n, err := repo.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n < 1 {
		t.Errorf("DeleteExpired removed %d rows, want at least the expired one", n)
	}
	got, err = repo.List(ctx, time.Unix(0, 0), now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range got {
		if r.SessionID == "suite-rev-expired" {
			t.Error("expired revocation survived DeleteExpired")
		}
	}
	stillThere := false
	for _, r := range got {
		if r.SessionID == "suite-rev-active" {
			stillThere = true
		}
	}
	if !stillThere {
		t.Error("DeleteExpired removed an unexpired revocation")
	}
}
