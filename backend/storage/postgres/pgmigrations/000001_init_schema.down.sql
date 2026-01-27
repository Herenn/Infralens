-- InfraLens Initial Schema Rollback for PostgreSQL
-- Migration: 000001_init_schema (down)
-- Description: Drops all core tables and functions

-- Drop triggers
DROP TRIGGER IF EXISTS update_services_updated_at ON services;
DROP TRIGGER IF EXISTS update_connections_updated_at ON connections;
DROP TRIGGER IF EXISTS update_node_metrics_updated_at ON node_metrics;

-- Drop function
DROP FUNCTION IF EXISTS update_updated_at_column();

-- Drop indexes
DROP INDEX IF EXISTS idx_services_node;
DROP INDEX IF EXISTS idx_services_namespace;
DROP INDEX IF EXISTS idx_services_last_seen;
DROP INDEX IF EXISTS idx_services_type;

DROP INDEX IF EXISTS idx_connections_source;
DROP INDEX IF EXISTS idx_connections_target;
DROP INDEX IF EXISTS idx_connections_last_seen;
DROP INDEX IF EXISTS idx_connections_port;

DROP INDEX IF EXISTS idx_node_metrics_last_seen;

-- Drop tables (order matters due to foreign keys)
DROP TABLE IF EXISTS service_inspections;
DROP TABLE IF EXISTS connections;
DROP TABLE IF EXISTS node_metrics;
DROP TABLE IF EXISTS services;
