-- InfraLens Initial Schema Rollback
-- Migration: 000001_init_schema (down)
-- Description: Drops all core tables

-- Drop indexes first
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
