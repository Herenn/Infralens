package service

import (
	"context"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

// endpointRefreshInterval throttles refreshEndpointsFromStats: at most one
// current-state write per service per interval. Well inside the default
// PRUNE_MAX_AGE (30m), so an endpoint of a connection carrying traffic is
// never pruned, while write volume tracks the number of live services rather
// than the throughput report rate.
const endpointRefreshInterval = time.Minute

// refreshEndpointsFromStats keeps both endpoints of a connection that is
// carrying traffic alive in current state.
//
// A service's last_seen used to move only on connect/accept events, while a
// connection's moved on every throughput report. A long-lived connection -
// a database pool, a gRPC channel, a Kafka consumer - opened once and then
// used for days therefore kept itself alive while both of its endpoints aged
// out at PRUNE_MAX_AGE and were deleted: the busiest dependencies in the
// system were reported as gone (and, by the idle-spend report, as
// decommission candidates). Throughput is proof both ends are alive.
//
// Only services that still exist are refreshed: nothing already pruned is
// resurrected, and nothing is synthesized from the connection. Re-upserting
// the stored row changes no field except last_seen (the upsert keeps
// existing values for anything empty). Runs whether or not history is
// enabled; refreshHistoryFromStats then extends their history as before.
func (ts *TopologyService) refreshEndpointsFromStats(ctx context.Context, conn *storage.Connection) {
	if conn == nil {
		return
	}
	now := time.Now()
	for _, id := range []string{conn.SourceID, conn.TargetID} {
		if !ts.claimEndpointRefresh(id, now) {
			continue
		}
		svc, err := ts.store.Services().Get(ctx, id)
		if err != nil || svc == nil {
			continue
		}
		_ = ts.store.Services().Upsert(ctx, svc)
	}
}

func (ts *TopologyService) claimEndpointRefresh(id string, now time.Time) bool {
	st := &ts.idle
	st.liveMu.Lock()
	defer st.liveMu.Unlock()
	if st.liveSeen == nil {
		st.liveSeen = map[string]time.Time{}
	}
	if last, ok := st.liveSeen[id]; ok && now.Sub(last) < endpointRefreshInterval {
		return false
	}
	st.liveSeen[id] = now
	if now.Sub(st.liveSwept) > 10*endpointRefreshInterval {
		for k, at := range st.liveSeen {
			if now.Sub(at) >= endpointRefreshInterval {
				delete(st.liveSeen, k)
			}
		}
		st.liveSwept = now
	}
	return true
}
