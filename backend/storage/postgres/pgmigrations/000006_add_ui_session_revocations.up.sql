-- Migration: 000006_add_ui_session_revocations
-- Description: Persists UI logouts so they survive restarts and reach every replica
--
-- See the SQLite migration of the same number for the rationale. Times are
-- unix seconds; created_at is assigned by the database (transaction start
-- time) so replicas sync incrementally without trusting each other's clocks.
--
-- Independent of migrations 000004/000005: touches no other table.

CREATE TABLE IF NOT EXISTS ui_session_revocations (
    session_id TEXT PRIMARY KEY,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::BIGINT)
);

CREATE INDEX IF NOT EXISTS idx_ui_session_revocations_created ON ui_session_revocations(created_at);
CREATE INDEX IF NOT EXISTS idx_ui_session_revocations_expires ON ui_session_revocations(expires_at);
