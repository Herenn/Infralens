-- Migration: 000002_add_protocol
-- Description: Adds protocol (tcp/udp) tracking to connections

ALTER TABLE connections ADD COLUMN protocol TEXT NOT NULL DEFAULT 'tcp';

CREATE INDEX IF NOT EXISTS idx_connections_protocol ON connections(protocol);
