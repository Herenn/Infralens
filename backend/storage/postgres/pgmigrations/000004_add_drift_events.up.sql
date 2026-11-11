-- Migration: 000004_add_drift_events
-- Description: Persists architecture drift as a feed of per-edge events
--
-- Drift detection (v3.0) compared history against what it had already seen
-- and posted the difference to a webhook - nothing was kept, so without a
-- webhook there was no way to ask "what changed this week". Each row here is
-- one edge that appeared (never seen before) or disappeared (no longer
-- observed), as the detector saw it at detected_at.
--
-- source_name / target_name are display-name snapshots taken at detection
-- time: a disappeared edge's endpoints have usually been pruned from current
-- state by the time someone reads about it, and unresolved peers have only an
-- IP for an ID. Empty means no better name than the ID was known.
--
-- fingerprint identifies the occurrence itself ("this edge first appeared at
-- T", "this edge was last seen at T"), not the detection of it, and is unique:
-- a restarted detector, or a second backend replica, that re-detects the same
-- change inserts nothing.
--
-- Pruned on the history retention window (HISTORY_RETENTION): an event is
-- only useful while the topology it refers to can still be reconstructed.

CREATE TABLE IF NOT EXISTS drift_events (
    id BIGSERIAL PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    detected_at TIMESTAMP WITH TIME ZONE NOT NULL,
    kind TEXT NOT NULL,
    connection_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    source_name TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL,
    target_name TEXT NOT NULL DEFAULT '',
    port INTEGER NOT NULL DEFAULT 0,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    external BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_drift_events_fingerprint
    ON drift_events(fingerprint);

-- The feed is read newest-first and paged by (detected_at, id).
CREATE INDEX IF NOT EXISTS idx_drift_events_detected
    ON drift_events(detected_at, id);

-- When the detector last completed a pass. On restart it resumes from here,
-- so a change that happened between the last pass and shutdown is still
-- reported rather than silently absorbed into the startup baseline.
CREATE TABLE IF NOT EXISTS drift_checkpoint (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    checked_at TIMESTAMP WITH TIME ZONE NOT NULL
);
