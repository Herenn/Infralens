-- InfraLens Initial Schema for PostgreSQL
-- Migration: 000001_init_schema
-- Description: Creates the core tables for services, connections, and metrics

-- ============================================================================
-- Services Table
-- Stores information about discovered services (processes/pods)
-- ============================================================================
CREATE TABLE IF NOT EXISTS services (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    display_name TEXT,
    resolved_name TEXT,
    type TEXT,
    tech TEXT,
    icon TEXT,
    namespace TEXT,
    node TEXT,
    pod_ip TEXT,
    labels JSONB DEFAULT '{}'::jsonb,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    healthy BOOLEAN DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- Service Inspections Table
-- Stores deep inspection data for services (protocol probing, dependencies, etc.)
-- ============================================================================
CREATE TABLE IF NOT EXISTS service_inspections (
    service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    pid INTEGER,
    process_name TEXT,
    command_line JSONB DEFAULT '[]'::jsonb,
    working_dir TEXT,
    env_var_names JSONB DEFAULT '[]'::jsonb,
    listen_ports JSONB DEFAULT '[]'::jsonb,
    config_files JSONB DEFAULT '[]'::jsonb,
    dependencies JSONB DEFAULT '[]'::jsonb,
    http_info JSONB,
    db_info JSONB,
    k8s_metadata JSONB,
    code_context JSONB,
    inspected_at TIMESTAMP WITH TIME ZONE NOT NULL
);

-- ============================================================================
-- Connections Table
-- Stores network connections between services
-- ============================================================================
CREATE TABLE IF NOT EXISTS connections (
    id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    port INTEGER NOT NULL,
    count BIGINT DEFAULT 1,
    bytes_sent BIGINT DEFAULT 0,
    bytes_recv BIGINT DEFAULT 0,
    bytes_sent_rate DOUBLE PRECISION DEFAULT 0,
    bytes_recv_rate DOUBLE PRECISION DEFAULT 0,
    packets_sent BIGINT DEFAULT 0,
    packets_recv BIGINT DEFAULT 0,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    latency_ms DOUBLE PRECISION DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- Node Metrics Table
-- Stores CPU/RAM metrics for physical/virtual nodes
-- ============================================================================
CREATE TABLE IF NOT EXISTS node_metrics (
    node_name TEXT PRIMARY KEY,
    cpu_percent DOUBLE PRECISION DEFAULT 0,
    mem_percent DOUBLE PRECISION DEFAULT 0,
    mem_used BIGINT DEFAULT 0,
    mem_total BIGINT DEFAULT 0,
    last_seen TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- Indexes for Query Performance
-- ============================================================================

-- Services indexes
CREATE INDEX IF NOT EXISTS idx_services_node ON services(node);
CREATE INDEX IF NOT EXISTS idx_services_namespace ON services(namespace);
CREATE INDEX IF NOT EXISTS idx_services_last_seen ON services(last_seen);
CREATE INDEX IF NOT EXISTS idx_services_type ON services(type);

-- Connections indexes
CREATE INDEX IF NOT EXISTS idx_connections_source ON connections(source_id);
CREATE INDEX IF NOT EXISTS idx_connections_target ON connections(target_id);
CREATE INDEX IF NOT EXISTS idx_connections_last_seen ON connections(last_seen);
CREATE INDEX IF NOT EXISTS idx_connections_port ON connections(port);

-- Node metrics indexes
CREATE INDEX IF NOT EXISTS idx_node_metrics_last_seen ON node_metrics(last_seen);

-- ============================================================================
-- Trigger for auto-updating updated_at
-- ============================================================================
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_services_updated_at
    BEFORE UPDATE ON services
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_connections_updated_at
    BEFORE UPDATE ON connections
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_node_metrics_updated_at
    BEFORE UPDATE ON node_metrics
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
