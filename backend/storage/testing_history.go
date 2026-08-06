package storage

import (
	"context"
	"testing"
	"time"
)

// HistoryRepoSuite exercises topology history against any Store implementation.
//
// The interval model is the part of history most likely to be subtly wrong, so
// these tests are written around the behaviour that matters rather than the
// storage mechanics: a continuously observed entity must stay one interval, a
// gap must split it into two, and reconstructing a past instant must reflect
// what was actually present then rather than what is present now.
func HistoryRepoSuite(t *testing.T, store Store) {
	ctx := context.Background()
	history := store.History()

	// Anchored well in the past so these rows never collide with, or get
	// pruned alongside, anything else the suite writes.
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	const maxGap = 5 * time.Minute

	t.Run("BoundsIsEmptyWithNoHistory", func(t *testing.T) {
		_, ok, err := history.Bounds(ctx)
		if err != nil {
			t.Fatalf("Bounds: %v", err)
		}
		if ok {
			t.Error("expected ok=false before any history has been recorded")
		}
	})

	t.Run("ContinuousObservationStaysOneInterval", func(t *testing.T) {
		svc := &Service{ID: "hist-continuous", Name: "api", Type: "web_server"}

		// Observed repeatedly, each sighting comfortably inside maxGap.
		for i := 0; i < 5; i++ {
			at := base.Add(time.Duration(i) * time.Minute)
			if err := history.RecordService(ctx, svc, at, maxGap); err != nil {
				t.Fatalf("recording service at +%dm: %v", i, err)
			}
		}

		// Present at the start, the middle, and the end of that stretch.
		for _, offset := range []time.Duration{0, 2 * time.Minute, 4 * time.Minute} {
			at := base.Add(offset)
			got, err := history.ServicesAt(ctx, at)
			if err != nil {
				t.Fatalf("querying services at +%s: %v", offset, err)
			}
			if !containsService(got, "hist-continuous") {
				t.Errorf("service missing from topology at +%s", offset)
			}
		}

		// One unbroken interval, not five.
		intervals := servicesWithID(mustServicesBetween(t, history, ctx,
			base.Add(-time.Hour), base.Add(time.Hour)), "hist-continuous")
		if len(intervals) != 1 {
			t.Fatalf("expected 1 merged interval for a continuously observed service, got %d", len(intervals))
		}
		if !intervals[0].FirstSeen.Equal(base) {
			t.Errorf("first_seen = %s, want %s", intervals[0].FirstSeen, base)
		}
		if !intervals[0].LastSeen.Equal(base.Add(4 * time.Minute)) {
			t.Errorf("last_seen = %s, want %s", intervals[0].LastSeen, base.Add(4*time.Minute))
		}
	})

	t.Run("GapSplitsIntoSeparateIntervals", func(t *testing.T) {
		svc := &Service{ID: "hist-gap", Name: "worker"}

		// Seen, then absent for well over maxGap, then seen again.
		if err := history.RecordService(ctx, svc, base, maxGap); err != nil {
			t.Fatalf("recording first sighting: %v", err)
		}
		returned := base.Add(30 * time.Minute)
		if err := history.RecordService(ctx, svc, returned, maxGap); err != nil {
			t.Fatalf("recording sighting after gap: %v", err)
		}

		intervals := servicesWithID(mustServicesBetween(t, history, ctx,
			base.Add(-time.Hour), base.Add(2*time.Hour)), "hist-gap")

		// ServicesBetween merges by ID for the caller's convenience, so the
		// absence is verified by querying the moment inside the gap.
		if len(intervals) == 0 {
			t.Fatal("expected the service to appear in the window")
		}

		duringGap := base.Add(15 * time.Minute)
		got, err := history.ServicesAt(ctx, duringGap)
		if err != nil {
			t.Fatalf("querying during gap: %v", err)
		}
		if containsService(got, "hist-gap") {
			t.Error("service present during a gap longer than maxGap; the interval should have been closed and a new one opened")
		}

		// Still present at both ends.
		if !containsService(mustServicesAt(t, history, ctx, base), "hist-gap") {
			t.Error("service missing at its first sighting")
		}
		if !containsService(mustServicesAt(t, history, ctx, returned), "hist-gap") {
			t.Error("service missing at its second sighting")
		}
	})

	t.Run("PastTopologyIsNotPresentTopology", func(t *testing.T) {
		// Something that existed early and stopped.
		retired := &Service{ID: "hist-retired", Name: "legacy"}
		if err := history.RecordService(ctx, retired, base, maxGap); err != nil {
			t.Fatalf("recording retired service: %v", err)
		}

		// Something that only showed up later.
		added := &Service{ID: "hist-added", Name: "new-thing"}
		laterOn := base.Add(2 * time.Hour)
		if err := history.RecordService(ctx, added, laterOn, maxGap); err != nil {
			t.Fatalf("recording added service: %v", err)
		}

		early := mustServicesAt(t, history, ctx, base)
		if !containsService(early, "hist-retired") {
			t.Error("retired service should be present at the earlier instant")
		}
		if containsService(early, "hist-added") {
			t.Error("service added later must not appear in an earlier reconstruction")
		}

		late := mustServicesAt(t, history, ctx, laterOn)
		if !containsService(late, "hist-added") {
			t.Error("added service should be present at the later instant")
		}
		if containsService(late, "hist-retired") {
			t.Error("retired service must not appear long after it stopped being observed")
		}
	})

	t.Run("ConnectionIntervals", func(t *testing.T) {
		conn := &Connection{
			ID:       "hist-a->hist-b:5432",
			SourceID: "hist-a",
			TargetID: "hist-b",
			Port:     5432,
			Protocol: "tcp",
		}

		for i := 0; i < 3; i++ {
			at := base.Add(time.Duration(i) * time.Minute)
			if err := history.RecordConnection(ctx, conn, at, maxGap); err != nil {
				t.Fatalf("recording connection at +%dm: %v", i, err)
			}
		}

		got, err := history.ConnectionsAt(ctx, base.Add(time.Minute))
		if err != nil {
			t.Fatalf("querying connections: %v", err)
		}

		var found *ConnectionInterval
		for i := range got {
			if got[i].ConnectionID == conn.ID {
				found = &got[i]
				break
			}
		}
		if found == nil {
			t.Fatal("connection missing from reconstructed topology")
		}
		if found.SourceID != "hist-a" || found.TargetID != "hist-b" {
			t.Errorf("edge endpoints wrong: %s -> %s", found.SourceID, found.TargetID)
		}
		if found.Port != 5432 {
			t.Errorf("port = %d, want 5432", found.Port)
		}
		if found.Protocol != "tcp" {
			t.Errorf("protocol = %q, want tcp", found.Protocol)
		}
	})

	t.Run("ProtocolDefaultsToTCP", func(t *testing.T) {
		conn := &Connection{
			ID:       "hist-noproto->hist-x:80",
			SourceID: "hist-noproto",
			TargetID: "hist-x",
			Port:     80,
			// Protocol deliberately empty: older agents omit it.
		}
		if err := history.RecordConnection(ctx, conn, base, maxGap); err != nil {
			t.Fatalf("recording connection without protocol: %v", err)
		}

		got := mustConnectionsAt(t, history, ctx, base)
		for _, ci := range got {
			if ci.ConnectionID == conn.ID && ci.Protocol != "tcp" {
				t.Errorf("protocol = %q, want it defaulted to tcp", ci.Protocol)
			}
		}
	})

	t.Run("RejectsInvalidInput", func(t *testing.T) {
		if err := history.RecordService(ctx, nil, base, maxGap); err == nil {
			t.Error("expected an error recording a nil service")
		}
		if err := history.RecordService(ctx, &Service{}, base, maxGap); err == nil {
			t.Error("expected an error recording a service with no ID")
		}
		if err := history.RecordConnection(ctx, nil, base, maxGap); err == nil {
			t.Error("expected an error recording a nil connection")
		}
		if err := history.RecordConnection(ctx, &Connection{}, base, maxGap); err == nil {
			t.Error("expected an error recording a connection with no ID")
		}
	})

	t.Run("DeleteStaleDropsOnlyEndedIntervals", func(t *testing.T) {
		old := &Service{ID: "hist-prunable", Name: "ancient"}
		oldAt := base.Add(-24 * time.Hour)
		if err := history.RecordService(ctx, old, oldAt, maxGap); err != nil {
			t.Fatalf("recording old service: %v", err)
		}

		recent := &Service{ID: "hist-keep", Name: "current"}
		recentAt := base.Add(time.Hour)
		if err := history.RecordService(ctx, recent, recentAt, maxGap); err != nil {
			t.Fatalf("recording recent service: %v", err)
		}

		// Cutoff between the two.
		cutoff := base
		if _, err := history.DeleteStale(ctx, cutoff); err != nil {
			t.Fatalf("pruning history: %v", err)
		}

		if containsService(mustServicesAt(t, history, ctx, oldAt), "hist-prunable") {
			t.Error("interval ending before the cutoff should have been pruned")
		}
		if !containsService(mustServicesAt(t, history, ctx, recentAt), "hist-keep") {
			t.Error("interval ending after the cutoff must be retained")
		}
	})

	t.Run("BoundsSpansEarliestAndLatestObservation", func(t *testing.T) {
		// Loose (>=/<=) rather than exact: earlier subtests in this suite have
		// already written rows sharing the same store, so Bounds legitimately
		// extends beyond what this subtest writes itself.
		early := base.Add(-48 * time.Hour)
		late := base.Add(48 * time.Hour)

		if err := history.RecordService(ctx, &Service{ID: "hist-bounds-early", Name: "early"}, early, maxGap); err != nil {
			t.Fatalf("recording early service: %v", err)
		}
		if err := history.RecordService(ctx, &Service{ID: "hist-bounds-late", Name: "late"}, late, maxGap); err != nil {
			t.Fatalf("recording late service: %v", err)
		}

		bounds, ok, err := history.Bounds(ctx)
		if err != nil {
			t.Fatalf("Bounds: %v", err)
		}
		if !ok {
			t.Fatal("expected ok=true once history has been recorded")
		}
		if bounds.Earliest.After(early) {
			t.Errorf("earliest bound %s is after the earliest recorded observation %s", bounds.Earliest, early)
		}
		if bounds.Latest.Before(late) {
			t.Errorf("latest bound %s is before the latest recorded observation %s", bounds.Latest, late)
		}
	})

	t.Run("StaleServicesFindsOnlyThingsNotSeenRecently", func(t *testing.T) {
		stale := &Service{ID: "hist-stale-old", Name: "old"}
		staleAt := base.Add(-96 * time.Hour)
		if err := history.RecordService(ctx, stale, staleAt, maxGap); err != nil {
			t.Fatalf("recording stale service: %v", err)
		}

		fresh := &Service{ID: "hist-stale-fresh", Name: "fresh"}
		freshAt := base.Add(-time.Hour)
		if err := history.RecordService(ctx, fresh, freshAt, maxGap); err != nil {
			t.Fatalf("recording fresh service: %v", err)
		}

		cutoff := base.Add(-48 * time.Hour)
		got, err := history.StaleServices(ctx, cutoff)
		if err != nil {
			t.Fatalf("StaleServices: %v", err)
		}

		if !containsService(got, "hist-stale-old") {
			t.Errorf("expected hist-stale-old (last seen %s, before cutoff %s) in stale services, got %+v", staleAt, cutoff, got)
		}
		if containsService(got, "hist-stale-fresh") {
			t.Errorf("hist-stale-fresh (last seen %s, after cutoff %s) should not be in stale services", freshAt, cutoff)
		}

		// One row per service - its newest interval, not every interval.
		if rows := servicesWithID(got, "hist-stale-old"); len(rows) != 1 {
			t.Errorf("expected exactly one row for hist-stale-old, got %d: %+v", len(rows), rows)
		}
	})
}

// --- helpers -------------------------------------------------------------

func containsService(intervals []ServiceInterval, id string) bool {
	for _, si := range intervals {
		if si.ServiceID == id {
			return true
		}
	}
	return false
}

func servicesWithID(intervals []ServiceInterval, id string) []ServiceInterval {
	var out []ServiceInterval
	for _, si := range intervals {
		if si.ServiceID == id {
			out = append(out, si)
		}
	}
	return out
}

func mustServicesAt(t *testing.T, h HistoryRepository, ctx context.Context, at time.Time) []ServiceInterval {
	t.Helper()
	got, err := h.ServicesAt(ctx, at)
	if err != nil {
		t.Fatalf("ServicesAt(%s): %v", at, err)
	}
	return got
}

func mustServicesBetween(t *testing.T, h HistoryRepository, ctx context.Context, from, to time.Time) []ServiceInterval {
	t.Helper()
	got, err := h.ServicesBetween(ctx, from, to)
	if err != nil {
		t.Fatalf("ServicesBetween(%s, %s): %v", from, to, err)
	}
	return got
}

func mustConnectionsAt(t *testing.T, h HistoryRepository, ctx context.Context, at time.Time) []ConnectionInterval {
	t.Helper()
	got, err := h.ConnectionsAt(ctx, at)
	if err != nil {
		t.Fatalf("ConnectionsAt(%s): %v", at, err)
	}
	return got
}
