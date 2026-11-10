package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// DriftRepoSuite exercises the drift event feed against any Store.
//
// Everything it writes is anchored at a base instant far in the past and
// fingerprinted "suite-drift-...", so it neither collides with nor is
// disturbed by rows other suites write, and every query is windowed to it.
func DriftRepoSuite(t *testing.T, store Store) {
	ctx := context.Background()
	drift := store.Drift()

	// Full nanosecond precision going in, as the detector's clock produces:
	// the store must reduce it to what it keeps (microseconds) and hand back
	// exactly that, or a value a client holds will not match what it reads.
	base := time.Now().Add(-200 * 24 * time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	stored := func(i int) time.Time { return DriftTime(base.Add(time.Duration(i) * time.Minute)) }
	window := func(f DriftEventFilter) DriftEventFilter {
		if f.Since.IsZero() {
			f.Since = base.Add(-time.Second)
		}
		if f.Until.IsZero() {
			f.Until = base.Add(24 * time.Hour)
		}
		return f
	}

	mk := func(i int, kind DriftEventKind, external bool) DriftEvent {
		at := base.Add(time.Duration(i) * time.Minute)
		return DriftEvent{
			DetectedAt:   at,
			Kind:         kind,
			ConnectionID: fmt.Sprintf("suite-src-%d->suite-dst-%d", i, i),
			SourceID:     fmt.Sprintf("suite-src-%d", i),
			SourceName:   fmt.Sprintf("source %d", i),
			TargetID:     fmt.Sprintf("suite-dst-%d", i),
			Port:         uint16(8000 + i),
			Protocol:     "tcp",
			External:     external,
			FirstSeen:    at.Add(-3 * time.Minute),
			LastSeen:     at.Add(-time.Minute),
			Fingerprint:  fmt.Sprintf("suite-drift-%d-%s", i, kind),
		}
	}

	// 0..5: alternating kinds; 1 and 4 internet-bound.
	var events []DriftEvent
	for i := 0; i < 6; i++ {
		kind := DriftAppeared
		if i%2 == 1 {
			kind = DriftDisappeared
		}
		events = append(events, mk(i, kind, i == 1 || i == 4))
	}

	t.Run("InsertAssignsIDsAndRoundTrips", func(t *testing.T) {
		inserted, err := drift.InsertEvents(ctx, events)
		if err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
		if len(inserted) != len(events) {
			t.Fatalf("expected %d inserted, got %d", len(events), len(inserted))
		}
		for i, ev := range inserted {
			if ev.ID == 0 {
				t.Errorf("event %s has no ID", ev.Fingerprint)
			}
			if !ev.DetectedAt.Equal(stored(i)) || ev.DetectedAt.Nanosecond()%1000 != 0 {
				t.Errorf("InsertEvents returned detected_at %s, want the stored form %s", ev.DetectedAt, stored(i))
			}
		}

		got, err := drift.ListEvents(ctx, window(DriftEventFilter{}))
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if len(got) != len(events) {
			t.Fatalf("expected %d events, got %d", len(events), len(got))
		}
		newest := got[0]
		want := events[5]
		if newest.Fingerprint != want.Fingerprint || newest.Kind != want.Kind ||
			newest.SourceID != want.SourceID || newest.SourceName != want.SourceName ||
			newest.TargetID != want.TargetID || newest.TargetName != "" ||
			newest.Port != want.Port || newest.Protocol != "tcp" || newest.External != want.External {
			t.Errorf("round trip mismatch:\n got %+v\nwant %+v", newest, want)
		}
		// What was returned at insert (and so published live) is exactly what
		// a later read returns.
		pub := inserted[5]
		if !newest.DetectedAt.Equal(pub.DetectedAt) || !newest.FirstSeen.Equal(pub.FirstSeen) || !newest.LastSeen.Equal(pub.LastSeen) {
			t.Errorf("read back %s/%s/%s, inserted as %s/%s/%s", newest.DetectedAt, newest.FirstSeen, newest.LastSeen,
				pub.DetectedAt, pub.FirstSeen, pub.LastSeen)
		}
		if !newest.DetectedAt.Equal(DriftTime(want.DetectedAt)) || !newest.FirstSeen.Equal(DriftTime(want.FirstSeen)) {
			t.Errorf("times mismatch: got detected=%s first=%s", newest.DetectedAt, newest.FirstSeen)
		}
	})

	t.Run("AfterIsExclusiveAtStoredPrecision", func(t *testing.T) {
		listed, _ := drift.ListEvents(ctx, window(DriftEventFilter{Limit: 1}))
		newest := listed[0].DetectedAt
		until := base.Add(24 * time.Hour)
		after, err := drift.ListEvents(ctx, DriftEventFilter{After: newest, Until: until})
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if len(after) != 0 {
			t.Errorf("After=<newest detected_at> returned %d events (the newest itself?), want 0", len(after))
		}
		sum, err := drift.Summarize(ctx, DriftEventFilter{After: newest, Until: until})
		if err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		if sum.Total != 0 {
			t.Errorf("Summarize After=<newest> counted %d, want 0 (the unread badge would never clear)", sum.Total)
		}
		just, _ := drift.ListEvents(ctx, DriftEventFilter{After: newest.Add(-time.Microsecond), Until: until})
		if len(just) != 1 {
			t.Errorf("After=<newest - 1us> returned %d events, want 1", len(just))
		}
	})

	t.Run("DuplicateFingerprintIsSkipped", func(t *testing.T) {
		dup := events[2]
		dup.DetectedAt = dup.DetectedAt.Add(time.Hour) // re-detected later, same occurrence
		fresh := mk(7, DriftAppeared, false)

		inserted, err := drift.InsertEvents(ctx, []DriftEvent{dup, fresh})
		if err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
		if len(inserted) != 1 || inserted[0].Fingerprint != fresh.Fingerprint {
			t.Fatalf("expected only the fresh event to be inserted, got %+v", inserted)
		}
		got, _ := drift.ListEvents(ctx, window(DriftEventFilter{}))
		if len(got) != len(events)+1 {
			t.Errorf("expected %d events after duplicate insert, got %d", len(events)+1, len(got))
		}
	})

	t.Run("NewestFirstAndFilters", func(t *testing.T) {
		got, err := drift.ListEvents(ctx, window(DriftEventFilter{}))
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		for i := 1; i < len(got); i++ {
			if got[i].DetectedAt.After(got[i-1].DetectedAt) {
				t.Fatalf("not newest first at %d: %s after %s", i, got[i].DetectedAt, got[i-1].DetectedAt)
			}
		}

		gone, _ := drift.ListEvents(ctx, window(DriftEventFilter{Kind: DriftDisappeared}))
		if len(gone) != 3 {
			t.Errorf("expected 3 disappeared, got %d", len(gone))
		}
		for _, ev := range gone {
			if ev.Kind != DriftDisappeared {
				t.Errorf("kind filter leaked %s", ev.Kind)
			}
		}

		yes := true
		ext, _ := drift.ListEvents(ctx, window(DriftEventFilter{External: &yes}))
		if len(ext) != 2 {
			t.Errorf("expected 2 external, got %d", len(ext))
		}
		no := false
		internal, _ := drift.ListEvents(ctx, window(DriftEventFilter{External: &no}))
		if len(internal) != len(got)-2 {
			t.Errorf("expected %d internal, got %d", len(got)-2, len(internal))
		}

		// Since is inclusive, Until exclusive.
		ranged, _ := drift.ListEvents(ctx, DriftEventFilter{Since: stored(2), Until: stored(4)})
		if len(ranged) != 2 || ranged[0].Fingerprint != events[3].Fingerprint || ranged[1].Fingerprint != events[2].Fingerprint {
			t.Errorf("expected events 3,2 in [base+2m, base+4m), got %v", fingerprints(ranged))
		}
	})

	t.Run("KeysetPagination", func(t *testing.T) {
		all, _ := drift.ListEvents(ctx, window(DriftEventFilter{}))

		// Two events at the same instant must still page deterministically.
		same := mk(8, DriftAppeared, false)
		same.DetectedAt = all[0].DetectedAt
		same.Fingerprint = "suite-drift-same-instant"
		if _, err := drift.InsertEvents(ctx, []DriftEvent{same}); err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
		all, _ = drift.ListEvents(ctx, window(DriftEventFilter{}))

		var paged []DriftEvent
		var cursor *DriftEventCursor
		for pages := 0; pages < 10; pages++ {
			page, err := drift.ListEvents(ctx, window(DriftEventFilter{Limit: 3, Before: cursor}))
			if err != nil {
				t.Fatalf("ListEvents page: %v", err)
			}
			paged = append(paged, page...)
			if len(page) < 3 {
				break
			}
			last := page[len(page)-1]
			cursor = &DriftEventCursor{DetectedAt: last.DetectedAt, ID: last.ID}
		}
		if fmt.Sprint(fingerprints(paged)) != fmt.Sprint(fingerprints(all)) {
			t.Errorf("paging lost or repeated events:\n paged %v\n   all %v", fingerprints(paged), fingerprints(all))
		}
	})

	t.Run("Summarize", func(t *testing.T) {
		sum, err := drift.Summarize(ctx, window(DriftEventFilter{}))
		if err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		all, _ := drift.ListEvents(ctx, window(DriftEventFilter{}))
		if sum.Total != int64(len(all)) || sum.Appeared+sum.Disappeared != sum.Total {
			t.Errorf("unexpected totals: %+v (listed %d)", sum, len(all))
		}
		if sum.Disappeared != 3 || sum.External != 2 {
			t.Errorf("unexpected breakdown: %+v", sum)
		}
		if sum.Latest == nil || !sum.Latest.Equal(all[0].DetectedAt) {
			t.Errorf("latest = %v, want %s", sum.Latest, all[0].DetectedAt)
		}

		empty, err := drift.Summarize(ctx, DriftEventFilter{Since: base.Add(-48 * time.Hour), Until: base.Add(-24 * time.Hour)})
		if err != nil {
			t.Fatalf("Summarize empty: %v", err)
		}
		if empty.Total != 0 || empty.Latest != nil {
			t.Errorf("expected empty summary, got %+v", empty)
		}
	})

	t.Run("DeleteBefore", func(t *testing.T) {
		n, err := drift.DeleteBefore(ctx, stored(3))
		if err != nil {
			t.Fatalf("DeleteBefore: %v", err)
		}
		if n < 3 {
			t.Errorf("expected at least the 3 events before base+3m deleted, got %d", n)
		}
		left, _ := drift.ListEvents(ctx, window(DriftEventFilter{}))
		for _, ev := range left {
			if ev.DetectedAt.Before(stored(3)) {
				t.Errorf("event %s survived the cutoff", ev.Fingerprint)
			}
		}
	})

	t.Run("CheckpointIsMonotonic", func(t *testing.T) {
		later := time.Now().Add(time.Hour).Truncate(time.Second)
		earlier := later.Add(-30 * time.Minute)

		if err := drift.SetCheckpoint(ctx, later); err != nil {
			t.Fatalf("SetCheckpoint: %v", err)
		}
		if err := drift.SetCheckpoint(ctx, earlier); err != nil {
			t.Fatalf("SetCheckpoint (earlier): %v", err)
		}
		at, ok, err := drift.Checkpoint(ctx)
		if err != nil || !ok {
			t.Fatalf("Checkpoint: ok=%v err=%v", ok, err)
		}
		if !at.Equal(later) {
			t.Errorf("checkpoint moved backwards: got %s, want %s", at, later)
		}
		if err := drift.SetCheckpoint(ctx, later.Add(time.Minute)); err != nil {
			t.Fatalf("SetCheckpoint (advance): %v", err)
		}
		at, _, _ = drift.Checkpoint(ctx)
		if !at.Equal(later.Add(time.Minute)) {
			t.Errorf("checkpoint did not advance: got %s", at)
		}
	})
}

func fingerprints(events []DriftEvent) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Fingerprint)
	}
	return out
}
