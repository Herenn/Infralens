package storage

import (
	"net/url"
	"regexp"
)

// libpqPasswordPattern matches a password=value pair in libpq's keyword/value
// DSN format (e.g. "host=db user=app password=hunter2 dbname=infralens"),
// where value is either unquoted (no spaces) or single-quoted.
var libpqPasswordPattern = regexp.MustCompile(`(?i)(password=)(?:'[^']*'|[^\s']+)`)

// RedactDSN returns dsn with any embedded credential replaced, safe to place
// in a log line. A Postgres DSN commonly carries its password directly
// (postgres://user:pass@host/db, or the libpq host=... password=... form);
// logging it verbatim put the database password in every log aggregator that
// ingested the process's output. A SQLite DSN is just a file path and passes
// through unchanged.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return dsn
	}

	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "REDACTED")
			return u.String()
		}
	}

	return libpqPasswordPattern.ReplaceAllString(dsn, "${1}REDACTED")
}
