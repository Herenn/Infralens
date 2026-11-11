package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ParitySuite pins behaviour the drift detector and the coverage model rely
// on, which the CI Postgres job must exercise as well as SQLite: streamed
// history reads, node (agent heartbeat) intervals, history and drift
// pruning, the unconditional checkpoint reset, and UTC/microsecond times on
// the way out.
func ParitySuite(t *testing.T, store Store) {
	ctx := context.Background()
	h := store.History()
	tag := fmt.Sprintf("parity-%d", time.Now().UnixNano())
	base := time.Now().Add(-6 * time.Hour).UTC().Truncate(time.Second)
	const gap = 5 * time.Minute

	t.Run("EachConnectionBetweenStreamsInFirstSeenOrder", func(t *testing.T) {
		mk := func(n string) *Connection {
			return &Connection{ID: tag + "-" + n, SourceID: tag + "-src", TargetID: tag + "-" + n, Port: 443,
				Hostnames: ObservedHostnames(base, n+".example.com")}
		}
		// b first seen before a; c outside the window.
		for _, o := range []struct {
			c  *Connection
			at time.Duration
		}{{mk("b"), 0}, {mk("a"), time.Minute}, {mk("c"), 5 * time.Hour}} {
			if err := h.RecordConnection(ctx, o.c, base.Add(o.at), gap); err != nil {
				t.Fatal(err)
			}
		}
		var got []ConnectionInterval
		err := h.EachConnectionBetween(ctx, base.Add(-time.Minute), base.Add(2*time.Minute), func(ci ConnectionInterval) error {
			if ci.SourceID == tag+"-src" {
				got = append(got, ci)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ConnectionID != tag+"-b" || got[1].ConnectionID != tag+"-a" {
			t.Fatalf("streamed %+v, want b then a", got)
		}
		if len(got[0].Hostnames) != 1 || got[0].Hostnames[0].Name != "b.example.com" {
			t.Errorf("hostnames not streamed: %+v", got[0].Hostnames)
		}
		if got[0].FirstSeen.Location() != time.UTC || !got[0].FirstSeen.Equal(base) {
			t.Errorf("first_seen = %v, want %v in UTC", got[0].FirstSeen, base)
		}
		stop := fmt.Errorf("stop")
		if err := h.EachConnectionBetween(ctx, base.Add(-time.Minute), base.Add(2*time.Minute), func(ConnectionInterval) error { return stop }); err != stop {
			t.Errorf("callback error not returned: %v", err)
		}
	})

	t.Run("NodeIntervalsExtendSplitAndPrune", func(t *testing.T) {
		node := tag + "-node"
		for _, at := range []time.Duration{0, time.Minute, 2 * time.Minute, 2 * time.Hour} {
			if err := h.RecordNode(ctx, node, base.Add(at), gap); err != nil {
				t.Fatal(err)
			}
		}
		got, err := h.NodesBetween(ctx, base.Add(-time.Hour), base.Add(3*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		var mine []NodeInterval
		for _, ni := range got {
			if ni.Node == node {
				mine = append(mine, ni)
			}
		}
		if len(mine) != 2 || !mine[0].FirstSeen.Equal(base) || !mine[0].LastSeen.Equal(base.Add(2*time.Minute)) ||
			!mine[1].FirstSeen.Equal(base.Add(2*time.Hour)) {
			t.Fatalf("node intervals = %+v, want [base, base+2m] and [base+2h, base+2h]", mine)
		}
		if mine[0].FirstSeen.Location() != time.UTC {
			t.Errorf("node interval times not UTC: %v", mine[0].FirstSeen.Location())
		}
		if err := h.RecordNode(ctx, "", base, gap); err == nil {
			t.Error("RecordNode accepted an empty node name")
		}
		// Pruning drops the ended interval, keeps the later one.
		if _, err := h.DeleteStale(ctx, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, _ = h.NodesBetween(ctx, base.Add(-time.Hour), base.Add(3*time.Hour))
		n := 0
		for _, ni := range got {
			if ni.Node == node {
				n++
			}
		}
		if n != 1 {
			t.Errorf("after DeleteStale %d node intervals, want 1", n)
		}
	})

	t.Run("TimesAreMicrosecondsTruncatedNotRounded", func(t *testing.T) {
		at := base.Add(time.Hour).Add(999 * time.Nanosecond) // rounds up, truncates down
		svc := &Service{ID: tag + "-precise"}
		if err := h.RecordService(ctx, svc, at, gap); err != nil {
			t.Fatal(err)
		}
		got, err := h.ServicesBetween(ctx, at.Add(-time.Second), at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		for _, si := range got {
			if si.ServiceID == svc.ID {
				if want := at.Truncate(time.Microsecond); !si.FirstSeen.Equal(want) {
					t.Errorf("stored %v, want %v (truncated, as both stores do)", si.FirstSeen, want)
				}
				return
			}
		}
		t.Fatal("interval not found")
	})

	t.Run("DriftCheckpointResetMovesBackwards", func(t *testing.T) {
		d := store.Drift()
		later, earlier := base.Add(3*time.Hour), base.Add(time.Hour)
		// Start from a known checkpoint whatever earlier suites left.
		if err := d.ResetCheckpoint(ctx, base); err != nil {
			t.Fatal(err)
		}
		if err := d.SetCheckpoint(ctx, later); err != nil {
			t.Fatal(err)
		}
		if err := d.SetCheckpoint(ctx, earlier); err != nil {
			t.Fatal(err)
		}
		if at, _, _ := d.Checkpoint(ctx); !at.Equal(later) {
			t.Fatalf("SetCheckpoint moved backwards to %v", at)
		}
		if err := d.ResetCheckpoint(ctx, earlier); err != nil {
			t.Fatal(err)
		}
		at, ok, err := d.Checkpoint(ctx)
		if err != nil || !ok || !at.Equal(earlier) || at.Location() != time.UTC {
			t.Errorf("after ResetCheckpoint: %v ok=%v err=%v, want %v in UTC", at, ok, err, earlier)
		}
	})

	t.Run("DriftPruneDropsOnlyOlderEvents", func(t *testing.T) {
		d := store.Drift()
		old := DriftEvent{DetectedAt: base.Add(-48 * time.Hour), Kind: DriftAppeared, ConnectionID: tag + "-old",
			SourceID: "s", TargetID: "t", FirstSeen: base, LastSeen: base, Fingerprint: tag + "-old"}
		recent := old
		recent.DetectedAt, recent.ConnectionID, recent.Fingerprint = base, tag+"-new", tag+"-new"
		if _, err := d.InsertEvents(ctx, []DriftEvent{old, recent}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DeleteBefore(ctx, base.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		evs, err := d.ListEvents(ctx, DriftEventFilter{Since: base.Add(-72 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		var have []string
		for _, ev := range evs {
			if ev.ConnectionID == old.ConnectionID || ev.ConnectionID == recent.ConnectionID {
				have = append(have, ev.ConnectionID)
				if ev.DetectedAt.Location() != time.UTC {
					t.Errorf("detected_at not UTC: %v", ev.DetectedAt.Location())
				}
			}
		}
		if len(have) != 1 || have[0] != recent.ConnectionID {
			t.Errorf("after DeleteBefore: %v, want only %s", have, recent.ConnectionID)
		}
	})
}

// Migrator is implemented by stores whose schema can be moved to a version.
type Migrator interface {
	MigrateTo(version uint) error
}

// MigrationRoundTripSuite migrates a store down to v3.0's schema (version 3)
// - executing every v3.1 down migration - and back up to latest, checking
// the v3.1 tables come and go. Run it on a store no other test is using.
func MigrationRoundTripSuite(t *testing.T, store Store, m Migrator, latest uint) {
	ctx := context.Background()
	usable := func() error {
		if _, err := store.Drift().ListEvents(ctx, DriftEventFilter{Limit: 1}); err != nil {
			return err
		}
		if _, err := store.History().NodesBetween(ctx, time.Now().Add(-time.Hour), time.Now()); err != nil {
			return err
		}
		if rs, ok := store.(SessionRevocationStore); ok {
			if _, err := rs.SessionRevocations().List(ctx, time.Unix(0, 0), time.Now()); err != nil {
				return err
			}
		}
		_, err := store.Connections().List(ctx, ConnectionFilter{})
		return err
	}
	if err := usable(); err != nil {
		t.Fatalf("at latest before the round trip: %v", err)
	}
	if err := m.MigrateTo(3); err != nil {
		t.Fatalf("migrating down to 3: %v", err)
	}
	if _, err := store.Drift().ListEvents(ctx, DriftEventFilter{Limit: 1}); err == nil {
		t.Error("drift_events still usable at version 3")
	}
	if _, err := store.History().NodesBetween(ctx, time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Error("node_intervals still usable at version 3")
	}
	// v3.0 reads still work at version 3: connections without hostnames.
	if _, err := store.Services().List(ctx, ServiceFilter{}); err != nil {
		t.Errorf("services unusable at version 3: %v", err)
	}
	if err := m.MigrateTo(latest); err != nil {
		t.Fatalf("migrating back up to %d: %v", latest, err)
	}
	if err := usable(); err != nil {
		t.Errorf("after the round trip: %v", err)
	}
}
