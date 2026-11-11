package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// TestHistoryQueriesAreZoneIndependent: history is written in local time and
// this driver compares DATETIME as text, so a query instant in another zone
// (the UI always sends ?at= in UTC) used to be shifted by the zone offset and
// miss everything. The "other" zone here is always 5 hours off local, so the
// test means the same thing on any host.
func TestHistoryQueriesAreZoneIndependent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	h := store.History().(*HistoryRepo)

	seen := time.Now().Add(-time.Hour)
	if err := h.RecordService(ctx, &storage.Service{ID: "svc"}, seen, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordConnection(ctx, &storage.Connection{ID: "a->b", SourceID: "a", TargetID: "b"}, seen, 5*time.Minute); err != nil {
		t.Fatal(err)
	}

	_, offset := seen.Zone()
	elsewhere := time.FixedZone("elsewhere", offset+5*3600)
	at := seen.Add(time.Minute).In(elsewhere)

	if got, _ := h.ServicesAt(ctx, at, 5*time.Minute); len(got) != 1 {
		t.Errorf("ServicesAt in another zone: got %d, want 1", len(got))
	}
	if got, _ := h.ConnectionsAt(ctx, at, 5*time.Minute); len(got) != 1 {
		t.Errorf("ConnectionsAt in another zone: got %d, want 1", len(got))
	}
	from, to := seen.Add(-time.Minute).In(elsewhere), seen.Add(time.Minute).In(elsewhere)
	if got, _ := h.ServicesBetween(ctx, from, to); len(got) != 1 {
		t.Errorf("ServicesBetween in another zone: got %d, want 1", len(got))
	}
	if got, _ := h.ConnectionsBetween(ctx, from, to); len(got) != 1 {
		t.Errorf("ConnectionsBetween in another zone: got %d, want 1", len(got))
	}
	if got, _ := h.StaleServices(ctx, seen.Add(time.Minute).In(elsewhere), 0); len(got) != 1 {
		t.Errorf("StaleServices in another zone: got %d, want 1", len(got))
	}
	if got, _ := h.LatestConnectionIntervals(ctx, from, to); len(got) != 1 {
		t.Errorf("LatestConnectionIntervals in another zone: got %d, want 1", len(got))
	}
	var streamed int
	_ = h.EachConnectionBetween(ctx, from, to, func(storage.ConnectionInterval) error { streamed++; return nil })
	if streamed != 1 {
		t.Errorf("EachConnectionBetween in another zone: got %d, want 1", streamed)
	}
	if n, _ := h.DeleteStale(ctx, seen.Add(-time.Minute).In(elsewhere)); n != 0 {
		t.Errorf("DeleteStale before the interval deleted %d rows", n)
	}
}
