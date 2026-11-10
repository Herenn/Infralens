-- Migration: 000004_add_drift_events (down)

DROP TABLE IF EXISTS drift_checkpoint;

DROP INDEX IF EXISTS idx_drift_events_detected;
DROP INDEX IF EXISTS idx_drift_events_fingerprint;
DROP TABLE IF EXISTS drift_events;
