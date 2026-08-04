-- Rollback: 000002_add_protocol

DROP INDEX IF EXISTS idx_connections_protocol;

ALTER TABLE connections DROP COLUMN IF EXISTS protocol;
