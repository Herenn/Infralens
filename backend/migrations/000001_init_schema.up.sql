-- InfraLens Initial Schema
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
    labels TEXT,  -- JSON: map[string]string
    last_seen DATETIME NOT NULL,
    healthy INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- Service Inspections Table
-- Stores deep inspection data for services (protocol probing, dependencies, etc.)
-- ============================================================================
CREATE TABLE IF NOT EXISTS service_inspections (
    service_id TEXT PRIMARY KEY,
    pid INTEGER,
    process_name TEXT,
    command_line TEXT,     -- JSON: []string
    working_dir TEXT,
    env_var_names TEXT,    -- JSON: []string
    listen_ports TEXT,     -- JSON: []int
    config_files TEXT,     -- JSON: []string
    dependencies TEXT,     -- JSON: []Dependency
    http_info TEXT,        -- JSON: HTTPProbeInfo
    db_info TEXT,          -- JSON: DBProbeInfo
    k8s_metadata TEXT,     -- JSON: K8sMetadataInfo
    code_context TEXT,     -- JSON: CodeContext
    inspected_at DATETIME NOT NULL,
    FOREIGN KEY (service_id) REFERENCES services(id) ON DELETE CASCADE
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
    count INTEGER DEFAULT 1,
    bytes_sent INTEGER DEFAULT 0,
    bytes_recv INTEGER DEFAULT 0,
    bytes_sent_rate REAL DEFAULT 0,
    bytes_recv_rate REAL DEFAULT 0,
    packets_sent INTEGER DEFAULT 0,
    packets_recv INTEGER DEFAULT 0,
    last_seen DATETIME NOT NULL,
    latency_ms REAL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- Node Metrics Table
-- Stores CPU/RAM metrics for physical/virtual nodes
-- ============================================================================
CREATE TABLE IF NOT EXISTS node_metrics (
    node_name TEXT PRIMARY KEY,
    cpu_percent REAL DEFAULT 0,
    mem_percent REAL DEFAULT 0,
    mem_used INTEGER DEFAULT 0,
    mem_total INTEGER DEFAULT 0,
    last_seen DATETIME NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
