-- Migration: 000007_add_node_intervals (rollback)
DROP INDEX IF EXISTS idx_node_intervals_range;
DROP INDEX IF EXISTS idx_node_intervals_group;
DROP INDEX IF EXISTS idx_node_intervals_lookup;
DROP TABLE IF EXISTS node_intervals;
