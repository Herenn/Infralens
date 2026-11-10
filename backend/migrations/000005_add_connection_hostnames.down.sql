-- Rollback: 000005_add_connection_hostnames

ALTER TABLE connection_intervals DROP COLUMN hostnames;

ALTER TABLE connections DROP COLUMN hostnames;
