-- Migration: 000005_add_connection_hostnames
-- Description: DNS names the source resolved an edge's destination from
--
-- Stored per connection (and per history interval), not per service: one IP
-- can serve many names (CDNs, shared load balancers) and each caller asked for
-- its own, so the edge is where the observation honestly belongs. A service's
-- names are derived on read from its incoming edges.
--
-- Format: comma-separated "name|unixSeconds" entries (the name and when it was
-- last observed), most recently observed first, at most 8; names unseen for 7
-- days are dropped on the next write. Names are validated to the hostname
-- alphabet before storage, so neither separator can occur inside one. Empty
-- means "no name observed".

ALTER TABLE connections ADD COLUMN IF NOT EXISTS hostnames TEXT NOT NULL DEFAULT '';

ALTER TABLE connection_intervals ADD COLUMN IF NOT EXISTS hostnames TEXT NOT NULL DEFAULT '';
