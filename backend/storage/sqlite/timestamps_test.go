package sqlite

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// Regression tests for how SQLite stores time (see dbTime). Several swap
// time.Local to reproduce a host in a given zone; none of this package's
// tests run in parallel, so that is safe.

func withLocal(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	return loc
}

func rawTimes(t *testing.T, s *Store, table string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT CAST(first_seen AS TEXT) || ' | ' || CAST(last_seen AS TEXT) FROM ` + table + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestTimestamps_StoredAsUTCWithoutMonotonicSuffix(t *testing.T) {
	withLocal(t, "Europe/Istanbul")
	s := newTestStore(t)
	if err := s.History().RecordService(context.Background(), &storage.Service{ID: "svc"}, time.Now(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, raw := range rawTimes(t, s, "service_intervals") {
		if strings.Contains(raw, "m=") || strings.Count(raw, "+0000 UTC") != 2 {
			t.Errorf("stored %q, want UTC text without a monotonic reading", raw)
		}
	}
}

// DST fall-back, America/New_York 2026-11-01: 01:00-01:59 happens twice.
func TestTimestamps_DSTFallBack(t *testing.T) {
	withLocal(t, "America/New_York")
	s := newTestStore(t)
	ctx := context.Background()
	h := s.History()

	start := time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC) // 00:30 EDT
	end := time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC)   // 02:30 EST
	for at := start; !at.After(end); at = at.Add(time.Minute) {
		if err := h.RecordService(ctx, &storage.Service{ID: "svc"}, at.Local(), 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if rows := rawTimes(t, s, "service_intervals"); len(rows) != 1 {
		t.Errorf("one continuously observed service across the repeated hour: %d intervals, want 1", len(rows))
	}
	for at := start.Add(time.Minute); at.Before(end); at = at.Add(time.Minute) {
		if got, _ := h.ServicesAt(ctx, at, 5*time.Minute); len(got) != 1 {
			t.Fatalf("ServicesAt(%s) missed the continuously present service", at)
		}
	}

	// Observed 00:30-01:40 EDT, then gone.
	for at := time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC); !at.After(time.Date(2026, 11, 1, 5, 40, 0, 0, time.UTC)); at = at.Add(time.Minute) {
		_ = h.RecordService(ctx, &storage.Service{ID: "gone"}, at.Local(), 5*time.Minute)
	}
	later := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC) // 01:30 EST, 50 minutes after
	got, _ := h.ServicesAt(ctx, later, 5*time.Minute)
	for _, si := range got {
		if si.ServiceID == "gone" {
			t.Error("a service last seen 50 minutes earlier (grace 5m) is reported present in the repeated hour")
		}
	}
	stale, _ := h.StaleServices(ctx, later, 0)
	found := false
	for _, si := range stale {
		found = found || si.ServiceID == "gone"
	}
	if !found {
		t.Error("StaleServices misses a service last seen before the repeated hour")
	}
}

// The host's zone changes between runs - a database moved between a UTC
// container and a laptop, or TZ changed.
func TestTimestamps_ZoneChangeBetweenRuns(t *testing.T) {
	withLocal(t, "Europe/Istanbul")
	s := newTestStore(t)
	ctx := context.Background()
	h := s.History()
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	for i := 0; i <= 60; i++ {
		_ = h.RecordService(ctx, &storage.Service{ID: "svc"}, base.Add(time.Duration(i)*time.Minute).Local(), 5*time.Minute)
	}
	time.Local = time.UTC // "restarted" with TZ=UTC; withLocal's cleanup restores
	for i := 61; i <= 120; i++ {
		_ = h.RecordService(ctx, &storage.Service{ID: "svc"}, base.Add(time.Duration(i)*time.Minute).Local(), 5*time.Minute)
	}
	if rows := rawTimes(t, s, "service_intervals"); len(rows) != 1 {
		t.Errorf("after a zone change: %d intervals, want 1", len(rows))
	}
	for i := 1; i < 120; i++ {
		if got, _ := h.ServicesAt(ctx, base.Add(time.Duration(i)*time.Minute), 5*time.Minute); len(got) == 0 {
			t.Fatalf("ServicesAt missed the service at +%dm", i)
		}
	}
	b, _, _ := h.Bounds(ctx)
	if !b.Latest.Equal(base.Add(120 * time.Minute)) {
		t.Errorf("Bounds.Latest = %s, want %s", b.Latest.UTC(), base.Add(120*time.Minute))
	}
}

// Time travel to exactly the instant a single-sighting edge was last seen -
// what the Changes panel's "View when last seen" sends - in every zone.
func TestTimestamps_ExactInstant(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Istanbul", "America/New_York", "Asia/Kolkata"} {
		t.Run(zone, func(t *testing.T) {
			withLocal(t, zone)
			s := newTestStore(t)
			ctx := context.Background()
			seen := time.Now() // carries a monotonic reading, as the processor's does
			_ = s.History().RecordConnection(ctx, &storage.Connection{ID: "a->b", SourceID: "a", TargetID: "b"}, seen, 5*time.Minute)

			// As the UI gets it from the API (RFC 3339, microseconds) and sends it back.
			at, _ := time.Parse(time.RFC3339Nano, seen.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
			if got, _ := s.History().ConnectionsAt(ctx, at, 5*time.Minute); len(got) != 1 {
				t.Errorf("ConnectionsAt(exact last_seen) found %d edges, want 1", len(got))
			}
			if got, _ := s.History().ConnectionsAt(ctx, at.In(time.FixedZone("x", -7*3600)), 5*time.Minute); len(got) != 1 {
				t.Errorf("same instant in another zone found %d edges, want 1", len(got))
			}
		})
	}
}

// writeV30Rows stores rows the way v3.0 did: time.Time parameters in the
// process's local zone (time.Now() carries a monotonic reading, which the
// driver renders too), and SQLite's own CURRENT_TIMESTAMP for defaults.
func writeV30Rows(t *testing.T, s *Store, n int, base time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO connection_intervals (connection_id, source_id, target_id, port, protocol, first_seen, last_seen)
		VALUES (?, 'a', 'b', 80, 'tcp', ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	mono := time.Now()
	for i := 0; i < n; i++ {
		first := base.Add(time.Duration(i) * time.Minute).Local()
		last := mono.Add(first.Sub(mono) + 30*time.Second) // monotonic-bearing, like time.Now()
		if _, err := stmt.Exec(fmt.Sprintf("v30-%d", i), first, last); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if _, err := tx.Exec(`INSERT INTO services (id, name, last_seen) VALUES ('svc-v30', 'svc', ?)`, mono); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestTimestamps_NormalizesV30Rows is the upgrade path: a v3.0 database
// written on a non-UTC host, read by v3.1.
func TestTimestamps_NormalizesV30Rows(t *testing.T) {
	withLocal(t, "Asia/Kolkata")
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 9, 0, 0, 123456789, time.UTC)
	writeV30Rows(t, s, 2*normalizeBatch+17, base) // more than one batch

	n, err := s.NormalizeTimestamps(ctx)
	if err != nil {
		t.Fatalf("NormalizeTimestamps: %v", err)
	}
	if want := int64(2*normalizeBatch + 17 + 1); n < want {
		t.Errorf("normalized %d rows, want at least %d", n, want)
	}
	for _, raw := range rawTimes(t, s, "connection_intervals")[:5] {
		if strings.Contains(raw, "m=") || strings.Count(raw, "+0000 UTC") != 2 {
			t.Errorf("row not normalized: %q", raw)
		}
	}
	// The instants are unchanged (to the microsecond), and queries in any
	// zone now find them.
	got, err := s.History().ConnectionsAt(ctx, base.Add(10*time.Second), time.Minute)
	if err != nil || len(got) != 1 || got[0].ConnectionID != "v30-0" {
		t.Fatalf("ConnectionsAt after normalizing: %v %v", got, err)
	}
	if !got[0].FirstSeen.Equal(base.Truncate(time.Microsecond)) {
		t.Errorf("first_seen changed instant: %s, want %s", got[0].FirstSeen, base)
	}

	// Idempotent: a second run (every later start) rewrites nothing.
	again, err := s.NormalizeTimestamps(ctx)
	if err != nil || again != 0 {
		t.Errorf("second run normalized %d rows (err %v), want 0", again, err)
	}
}

// An interrupted run leaves a mix of converted and unconverted rows; the
// next run finishes the job without touching what was done.
func TestTimestamps_NormalizeResumesAfterInterruption(t *testing.T) {
	withLocal(t, "America/New_York")
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	writeV30Rows(t, s, 100, base)
	// "Interrupted" after converting the first 40 rows.
	if _, err := s.db.Exec(`UPDATE connection_intervals SET first_seen = ?, last_seen = ? WHERE rowid <= 40`,
		dbTime(base), dbTime(base)); err != nil {
		t.Fatal(err)
	}
	n, err := s.NormalizeTimestamps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 60+1 { // the 60 left, plus the service row
		t.Errorf("resumed run normalized %d rows, want 61", n)
	}
}

// TestTimestamps_NormalizeLargeDatabase measures normalization on a large
// v3.0 database. Opt-in (it writes many rows): INFRALENS_NORMALIZE_ROWS=N.
func TestTimestamps_NormalizeLargeDatabase(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("INFRALENS_NORMALIZE_ROWS"))
	if rows <= 0 {
		t.Skip("set INFRALENS_NORMALIZE_ROWS to run")
	}
	withLocal(t, "Europe/Istanbul")
	s, err := New(storage.Config{Driver: "sqlite", DSN: t.TempDir() + "/big.db", AutoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	writeV30Rows(t, s, rows, time.Now().Add(-30*24*time.Hour))
	start := time.Now()
	n, err := s.NormalizeTimestamps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	start = time.Now()
	again, _ := s.NormalizeTimestamps(context.Background())
	t.Logf("normalized %d rows in %s (%.0f rows/s); re-check of a normalized database: %d rows in %s",
		n, took, float64(n)/took.Seconds(), again, time.Since(start))
}
