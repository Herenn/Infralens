-- Migration: 000007_add_node_intervals
-- Description: When each node's agent was reporting (agent heartbeats)
--
-- Observation coverage - "was InfraLens actually watching this node?" - used
-- to be inferred from the service and connection intervals touching a node.
-- Those are also extended by the *other* end's agent (a client on node B
-- keeps a server on node A's edges and service row alive), so a node whose
-- own agent was dead still looked observed, and Idle spend and the drift feed
-- counted that outage as silence. Agents report host metrics every few
-- seconds; recording those reports as intervals per node gives each node a
-- coverage only its own agent can extend. Same extend-or-open model as the
-- other interval tables.
--
-- node_name is always the real agent node, never a CLUSTER_NAME group:
-- under CLUSTER_NAME every agent's services and edges are recorded under the
-- group name, so a group-level heartbeat would let one live agent keep the
-- whole group "observed". group_name ('' when ungrouped) says which group
-- the agent reports under; a group's coverage is derived from its members'
-- intervals (service/coverage.go).

CREATE TABLE IF NOT EXISTS node_intervals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_name TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL
);

-- An agent is identified by (group_name, node_name): two clusters may well
-- have nodes with the same name.
CREATE INDEX IF NOT EXISTS idx_node_intervals_lookup
    ON node_intervals(group_name, node_name, last_seen);

CREATE INDEX IF NOT EXISTS idx_node_intervals_range
    ON node_intervals(first_seen, last_seen);
