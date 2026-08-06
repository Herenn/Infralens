-- Migration: 000003_add_topology_history (down)

DROP INDEX IF EXISTS idx_connection_intervals_range;
DROP INDEX IF EXISTS idx_connection_intervals_lookup;
DROP TABLE IF EXISTS connection_intervals;

DROP INDEX IF EXISTS idx_service_intervals_range;
DROP INDEX IF EXISTS idx_service_intervals_lookup;
DROP TABLE IF EXISTS service_intervals;
