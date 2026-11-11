-- Migration: 000007_add_node_intervals
-- Description: When each node's agent was reporting (agent heartbeats)
--
-- See the SQLite migration of the same number for the rationale. node_name
-- is the real agent node; group_name its CLUSTER_NAME group ('' if none).

CREATE TABLE IF NOT EXISTS node_intervals (
    id BIGSERIAL PRIMARY KEY,
    node_name TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    first_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL
);

-- An agent is identified by (group_name, node_name): two clusters may well
-- have nodes with the same name.
CREATE INDEX IF NOT EXISTS idx_node_intervals_lookup
    ON node_intervals(group_name, node_name, last_seen);

CREATE INDEX IF NOT EXISTS idx_node_intervals_range
    ON node_intervals(first_seen, last_seen);
