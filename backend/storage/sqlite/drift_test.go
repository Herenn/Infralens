package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// TestDriftRepo_NanosecondCursor: this driver keeps full nanosecond
// precision and compares DATETIME as text, so the keyset cursor's equality
// test only works if a value read back is rendered identically when bound
// again - including for times that arrive in a non-UTC zone with a monotonic
// clock reading, which is what time.Now() produces.
func TestDriftRepo_NanosecondCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	at := time.Now().In(time.FixedZone("UTC+3", 3*3600)) // same instant for all: exercises the id tiebreak
	var events []storage.DriftEvent
	for i := 0; i < 5; i++ {
		events = append(events, storage.DriftEvent{
			DetectedAt:   at,
			Kind:         storage.DriftAppeared,
			ConnectionID: fmt.Sprintf("a->b%d", i),
			SourceID:     "a",
			TargetID:     fmt.Sprintf("b%d", i),
			FirstSeen:    at,
			LastSeen:     at,
			Fingerprint:  fmt.Sprintf("ns-%d", i),
		})
	}
	if _, err := store.Drift().InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	var seen []string
	var cursor *storage.DriftEventCursor
	for page := 0; page < 5; page++ {
		got, err := store.Drift().ListEvents(ctx, storage.DriftEventFilter{Limit: 2, Before: cursor})
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		for _, ev := range got {
			seen = append(seen, ev.Fingerprint)
		}
		if len(got) < 2 {
			break
		}
		last := got[len(got)-1]
		cursor = &storage.DriftEventCursor{DetectedAt: last.DetectedAt, ID: last.ID}
	}
	if fmt.Sprint(seen) != "[ns-4 ns-3 ns-2 ns-1 ns-0]" {
		t.Errorf("paging by (detected_at, id) at one nanosecond instant: got %v", seen)
	}
}

// TestDriftRepo_PrunedWithHistoryRetention: the feed is collected on the
// history retention window, by the same pruning pass.
func TestDriftRepo_PrunedWithHistoryRetention(t *testing.T) {
	store, err := New(storage.Config{
		Driver:           "sqlite",
		DSN:              ":memory:",
		AutoMigrate:      true,
		HistoryEnabled:   true,
		HistoryRetention: time.Hour,
	})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	now := time.Now()
	mk := func(fp string, at time.Time) storage.DriftEvent {
		return storage.DriftEvent{DetectedAt: at, Kind: storage.DriftDisappeared, ConnectionID: fp,
			SourceID: "a", TargetID: "b", FirstSeen: at, LastSeen: at, Fingerprint: fp}
	}
	if _, err := store.Drift().InsertEvents(ctx, []storage.DriftEvent{
		mk("old", now.Add(-2*time.Hour)),
		mk("recent", now.Add(-10*time.Minute)),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	if _, err := store.PruneHistoryForTest(ctx); err != nil {
		t.Fatalf("pruning: %v", err)
	}
	got, err := store.Drift().ListEvents(ctx, storage.DriftEventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 || got[0].Fingerprint != "recent" {
		t.Errorf("expected only the event inside retention to survive, got %v", got)
	}
}
