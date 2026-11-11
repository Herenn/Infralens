package sqlite

import (
	"testing"

	"github.com/Herenn/Infralens/backend/storage"
)

func TestSessionRevocations(t *testing.T) {
	storage.SessionRevocationSuite(t, newTestStore(t).SessionRevocations())
}
