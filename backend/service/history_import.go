package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// HistoryObservation is one past sighting of a service or a connection
// (exactly one of the two is set), for ImportHistory.
type HistoryObservation struct {
	At         time.Time
	Service    *storage.Service
	Connection *storage.Connection
}

// ImportHistory records past observations into topology history, in one
// transaction, through the same extend-or-open logic live recording uses -
// so observations closer together than the history max gap form one
// continuous interval. Current state and the event bus are not touched.
//
// For backfilling history that was not observed by this process: the demo
// simulator's synthetic past, or an importer.
func (ts *TopologyService) ImportHistory(ctx context.Context, observations []HistoryObservation) error {
	if !ts.historyEnabled {
		return ErrHistoryDisabled
	}
	history := ts.store.History()
	return ts.store.RunInTransaction(ctx, func(ctx context.Context) error {
		for _, obs := range observations {
			switch {
			case obs.Service != nil:
				if err := history.RecordService(ctx, obs.Service, obs.At, ts.historyMaxGap); err != nil {
					return fmt.Errorf("importing service %s: %w", obs.Service.ID, err)
				}
			case obs.Connection != nil:
				if err := history.RecordConnection(ctx, obs.Connection, obs.At, ts.historyMaxGap); err != nil {
					return fmt.Errorf("importing connection %s: %w", obs.Connection.ID, err)
				}
			}
		}
		return nil
	})
}

// HistoryMaxGap is how long an entity may go unobserved before its history
// interval ends - the spacing ImportHistory observations must stay under to
// form one continuous interval. Zero when history is disabled.
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
