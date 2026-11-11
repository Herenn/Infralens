-- Rollback: 000005_add_connection_hostnames

ALTER TABLE connection_intervals DROP COLUMN IF EXISTS hostnames;

ALTER TABLE connections DROP COLUMN IF EXISTS hostnames;
