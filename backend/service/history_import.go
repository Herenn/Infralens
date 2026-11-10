package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// HistorySpan is a service or a connection (exactly one of the two is set)
// continuously observed over [From, To], for ImportHistory.
type HistorySpan struct {
	From, To   time.Time
	Service    *storage.Service
	Connection *storage.Connection
}

// ImportHistory records history that was not observed by this process - the
// demo simulator's synthetic past, or an importer - without touching current
// state or the event bus.
//
// Each span becomes exactly one continuous interval, written through the
// same extend-or-open logic live recording uses: two writes per span (open at
// From, then extend to To with a gap covering the whole span), all in one
// transaction, so a failed import leaves nothing half-written. Spans are
// clamped into the retention window (with a margin for the prune interval,
// so the next prune does not delete what was just written) and to now;
// spans left empty are skipped. A span with To == From records a single
// sighting.
//
// Connection hostnames are stored as given: stamp them with the span's To,
// as a live agent would have last reported them then.
func (ts *TopologyService) ImportHistory(ctx context.Context, spans []HistorySpan) error {
	if !ts.historyEnabled {
		return ErrHistoryDisabled
	}
	history := ts.store.History()
	earliest := ts.ImportHistoryEarliest()
	now := time.Now()
	return ts.store.RunInTransaction(ctx, func(ctx context.Context) error {
		for _, sp := range spans {
			from, to := sp.From, sp.To
			if from.Before(earliest) {
				from = earliest
			}
			if to.After(now) {
				to = now
			}
			if to.Before(from) {
				continue
			}
			extendGap := to.Sub(from) + ts.historyMaxGap
			switch {
			case sp.Service != nil:
				if err := history.RecordService(ctx, sp.Service, from, ts.historyMaxGap); err != nil {
					return fmt.Errorf("importing service %s: %w", sp.Service.ID, err)
				}
				if to.After(from) {
					if err := history.RecordService(ctx, sp.Service, to, extendGap); err != nil {
						return fmt.Errorf("importing service %s: %w", sp.Service.ID, err)
					}
				}
			case sp.Connection != nil:
				if err := history.RecordConnection(ctx, sp.Connection, from, ts.historyMaxGap); err != nil {
					return fmt.Errorf("importing connection %s: %w", sp.Connection.ID, err)
				}
				if to.After(from) {
					if err := history.RecordConnection(ctx, sp.Connection, to, extendGap); err != nil {
						return fmt.Errorf("importing connection %s: %w", sp.Connection.ID, err)
					}
				}
			}
		}
		return nil
	})
}

// ImportHistoryEarliest is the earliest instant ImportHistory writes: 90% of
// the retention window back from now. Callers seeding events that refer to
// imported history (the demo's drift feed) use it to stay consistent with
// what was actually written.
func (ts *TopologyService) ImportHistoryEarliest() time.Time {
	retention := ts.historyRetention
	if retention <= 0 {
		retention = storage.DefaultHistoryRetention
	}
	return time.Now().Add(-retention * 9 / 10)
}

// HistoryMaxGap is how long an entity may go unobserved before its history
// interval ends. Zero when history is disabled.
func (ts *TopologyService) HistoryMaxGap() time.Duration {
	return ts.historyMaxGap
}

// RemoveConnection deletes a connection from current state and tells live
// subscribers, instead of waiting for it to age out through pruning. History
// is untouched: the edge simply stops being extended.
func (ts *TopologyService) RemoveConnection(ctx context.Context, id string) error {
	if err := ts.store.Connections().Delete(ctx, id); err != nil {
		return fmt.Errorf("deleting connection %s: %w", id, err)
	}
	if ts.eventBus != nil {
		ts.eventBus.Publish(Event{
			Type: EventConnectionDeleted,
			Data: map[string]string{"connection_id": id},
		})
	}
	return nil
}

// RemoveService is RemoveConnection for services.
func (ts *TopologyService) RemoveService(ctx context.Context, id string) error {
	if err := ts.store.Services().Delete(ctx, id); err != nil {
		return fmt.Errorf("deleting service %s: %w", id, err)
	}
	if ts.eventBus != nil {
		ts.eventBus.PublishServiceDeleted(id)
	}
	return nil
}
