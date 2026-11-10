package service

import (
	"context"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// BackfillServiceHistory records svc as continuously observed over
// [from, to], without touching current state. It exists for the demo
// simulator (and any future import tooling), which needs a believable past -
// "nothing has called this for 9 days" - on a store that started a minute ago.
//
// Two writes per entity, not one per maxGap: the first opens an interval at
// `from`, the second extends it to `to` by passing a gap that spans the
// whole range. from is clamped into the retention window so the next prune
// does not immediately delete what was just written. A no-op when history is
// disabled.
func (ts *TopologyService) BackfillServiceHistory(ctx context.Context, svc *storage.Service, from, to time.Time) error {
	if !ts.historyEnabled || svc == nil {
		return nil
	}
	from, to, ok := ts.backfillRange(from, to)
	if !ok {
		return nil
	}
	if err := ts.store.History().RecordService(ctx, svc, from, ts.historyMaxGap); err != nil {
		return err
	}
	return ts.store.History().RecordService(ctx, svc, to, to.Sub(from)+ts.historyMaxGap)
}

// BackfillConnectionHistory is BackfillServiceHistory for an edge.
func (ts *TopologyService) BackfillConnectionHistory(ctx context.Context, conn *storage.Connection, from, to time.Time) error {
	if !ts.historyEnabled || conn == nil {
		return nil
	}
	from, to, ok := ts.backfillRange(from, to)
	if !ok {
		return nil
	}
	if err := ts.store.History().RecordConnection(ctx, conn, from, ts.historyMaxGap); err != nil {
		return err
	}
	return ts.store.History().RecordConnection(ctx, conn, to, to.Sub(from)+ts.historyMaxGap)
}

// backfillRange clamps from to inside the retention window (with a margin
// for the prune interval) and reports whether anything is left to write.
func (ts *TopologyService) backfillRange(from, to time.Time) (time.Time, time.Time, bool) {
	retention := ts.historyRetention
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	earliest := time.Now().Add(-retention * 9 / 10)
	if from.Before(earliest) {
		from = earliest
	}
	if to.After(time.Now()) {
		to = time.Now()
	}
	return from, to, to.After(from)
}

// HasConnectionHistory reports whether any history interval within the
// retention window exists for the connection. Used by the demo to backfill a
// persisted store only once: backfilling again would move "past" end times
// forward on every restart.
func (ts *TopologyService) HasConnectionHistory(ctx context.Context, connID string) (bool, error) {
	if !ts.historyEnabled {
		return false, nil
	}
	retention := ts.historyRetention
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	now := time.Now()
	rows, err := latestConnectionIntervals(ctx, ts.store.History(), now.Add(-retention), now)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if r.ConnectionID == connID {
			return true, nil
		}
	}
	return false, nil
}
