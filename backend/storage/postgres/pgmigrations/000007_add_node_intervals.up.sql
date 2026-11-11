-- Migration: 000007_add_node_intervals
-- Description: When each node's agent was reporting (agent heartbeats)
--
-- See the SQLite migration of the same number for the rationale.

CREATE TABLE IF NOT EXISTS node_intervals (
    id BIGSERIAL PRIMARY KEY,
    node_name TEXT NOT NULL,
    first_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_node_intervals_lookup
    ON node_intervals(node_name, last_seen);

CREATE INDEX IF NOT EXISTS idx_node_intervals_range
    ON node_intervals(first_seen, last_seen);
