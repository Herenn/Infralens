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
-- other interval tables. Under CLUSTER_NAME the node is the group name, as
-- everywhere else.

CREATE TABLE IF NOT EXISTS node_intervals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    node_name TEXT NOT NULL,
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_node_intervals_lookup
    ON node_intervals(node_name, last_seen);

CREATE INDEX IF NOT EXISTS idx_node_intervals_range
    ON node_intervals(first_seen, last_seen);
