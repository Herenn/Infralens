-- Migration: 000006_add_ui_session_revocations
-- Description: Persists UI logouts so they survive restarts and reach every replica
--
-- UI sessions are stateless signed cookies; the only server-side state is the
-- list of sessions ended early by logout. Keeping it in memory meant a restart
-- (with a shared SESSION_SECRET) brought every logged-out session back, and a
-- logout on one replica was unknown to the others.
--
-- Times are unix seconds. created_at is assigned by the database so replicas
-- can sync incrementally without trusting each other's clocks. A row is only
-- needed until expires_at (the session is rejected after that anyway) and is
-- deleted by the regular pruner.
--
-- Independent of migrations 000004/000005: touches no other table.

CREATE TABLE IF NOT EXISTS ui_session_revocations (
    session_id TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s', 'now') AS INTEGER))
);

CREATE INDEX IF NOT EXISTS idx_ui_session_revocations_created ON ui_session_revocations(created_at);
CREATE INDEX IF NOT EXISTS idx_ui_session_revocations_expires ON ui_session_revocations(expires_at);
