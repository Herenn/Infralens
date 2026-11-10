package postgres

import (
	"context"
	"testing"

	"github.com/Herenn/Infralens/backend/storage"
)

func TestSessionRevocations(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(),
			"DELETE FROM ui_session_revocations WHERE session_id LIKE 'suite-rev-%'")
	})
	storage.SessionRevocationSuite(t, store.SessionRevocations())
}
