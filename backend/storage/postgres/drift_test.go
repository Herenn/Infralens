package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// TestPostgresDriftConcurrentDuplicateInsert: several backend replicas share
// one Postgres and each detects the same change at about the same time. The
// unique fingerprint must let exactly one of them record (and so announce)
// it, without any of them erroring.
func TestPostgresDriftConcurrentDuplicateInsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	at := time.Now().Add(-90 * 24 * time.Hour).Truncate(time.Microsecond)
	ev := storage.DriftEvent{
		DetectedAt: at, Kind: storage.DriftAppeared, ConnectionID: "suite-race->dst",
		SourceID: "suite-race", TargetID: "dst", Port: 443, External: true,
		FirstSeen: at, LastSeen: at, Fingerprint: "suite-drift-race",
	}

	const replicas = 8
	var wg sync.WaitGroup
	results := make(chan int, replicas)
	errs := make(chan error, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mine := ev
			mine.DetectedAt = at.Add(time.Duration(i) * time.Millisecond)
			inserted, err := store.Drift().InsertEvents(ctx, []storage.DriftEvent{mine})
			if err != nil {
				errs <- err
				return
			}
			results <- len(inserted)
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Errorf("concurrent insert failed: %v", err)
	}
	total := 0
	for n := range results {
		total += n
	}
	if total != 1 {
		t.Errorf("%d replicas recorded the same change, want exactly 1", total)
	}

	got, err := store.Drift().ListEvents(ctx, storage.DriftEventFilter{Since: at.Add(-time.Second), Until: at.Add(time.Second)})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 || !got[0].External || got[0].Port != 443 {
		t.Errorf("stored events: %+v", got)
	}
}
