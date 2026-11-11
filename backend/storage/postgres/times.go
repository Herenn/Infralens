package postgres

import "time"

// pgTime is the form every time takes on its way into this store: UTC,
// truncated to microseconds. Postgres keeps microseconds but *rounds* finer
// values, while the SQLite store truncates (dbTime there); truncating here
// first makes both stores return exactly the same instant for the same
// input, so a value read back from one table compares equal to the same
// instant written elsewhere, whichever store is in use.
func pgTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// fromDB normalises a timestamptz read back from Postgres. lib/pq returns
// them in the server session's time zone (and a zone-aware Location), so two
// equal instants can print and serialise differently depending on server
// configuration; every time leaving this store is UTC, as the SQLite store
// returns them.
func fromDB(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}
