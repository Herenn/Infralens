package postgres

import "github.com/Herenn/Infralens/backend/storage"

// Compile-time checks that this store implements every storage interface,
// including the optional ones features discover by type assertion
// (SessionRevocationStore for persisted UI logouts,
// LatestConnectionIntervaler for idle-spend evidence). A store that silently
// lost one would degrade a feature at runtime instead of failing the build.
// The drift feed (DriftRepository) and streaming history reads
// (HistoryRepository.EachConnectionBetween) are part of the required
// interfaces. Store.History() must keep returning *HistoryRepo for the
// optional history extension to be found - see TestStoreOptionalInterfaces.
var (
	_ storage.Store                       = (*Store)(nil)
	_ storage.SessionRevocationStore      = (*Store)(nil)
	_ storage.ServiceRepository           = (*ServiceRepo)(nil)
	_ storage.ConnectionRepository        = (*ConnectionRepo)(nil)
	_ storage.MetricsRepository           = (*MetricsRepo)(nil)
	_ storage.HistoryRepository           = (*HistoryRepo)(nil)
	_ storage.LatestConnectionIntervaler  = (*HistoryRepo)(nil)
	_ storage.DriftRepository             = (*DriftRepo)(nil)
	_ storage.SessionRevocationRepository = (*SessionRevocationRepo)(nil)
)
