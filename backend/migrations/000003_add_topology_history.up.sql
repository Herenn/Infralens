-- Migration: 000003_add_topology_history
-- Description: Stores topology as temporal intervals so history survives pruning
--
-- The services/connections tables hold current state only: one mutable row per
-- entity, hard-deleted once it goes stale. That answers "what is talking to
-- what right now" and nothing else.
--
-- These tables record *when* each service and edge existed, as contiguous
-- [first_seen, last_seen] intervals. A service observed continuously is a
-- single row whose last_seen is bumped; a new row is only written when an
-- entity appears for the first time or reappears after a gap. Write volume is
-- therefore proportional to how often the architecture changes, not to how
-- often agents report - which is what keeps this viable on SQLite instead of
-- requiring a dedicated time-series store.
--
-- "Topology at time T" is a range query over these intervals. Change detection
-- falls out of the same shape: a new row is something appearing, an interval
-- that stopped being extended is something disappearing.

CREATE TABLE IF NOT EXISTS service_intervals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL DEFAULT '',
    tech TEXT NOT NULL DEFAULT '',
    namespace TEXT NOT NULL DEFAULT '',
    node TEXT NOT NULL DEFAULT '',
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL
);

-- Extend-or-open looks up the newest interval for one entity.
CREATE INDEX IF NOT EXISTS idx_service_intervals_lookup
    ON service_intervals(service_id, last_seen);

-- Point-in-time and range reconstruction scan by time.
CREATE INDEX IF NOT EXISTS idx_service_intervals_range
    ON service_intervals(first_seen, last_seen);

CREATE TABLE IF NOT EXISTS connection_intervals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    connection_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 0,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_connection_intervals_lookup
    ON connection_intervals(connection_id, last_seen);

CREATE INDEX IF NOT EXISTS idx_connection_intervals_range
    ON connection_intervals(first_seen, last_seen);
