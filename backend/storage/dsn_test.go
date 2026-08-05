package storage

import "testing"

// TestRedactDSN is a regression test: the backend used to log
// cfg.Storage.DSN verbatim at startup regardless of driver. A Postgres DSN
// commonly carries its password directly, so every log aggregator that
// ingested the process's stdout received the database credential.
func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		mustNotHave string
		wantExact   string // if set, output must equal this exactly
	}{
		{
			name:        "postgres URL form",
			dsn:         "postgres://appuser:hunter2@db.internal:5432/infralens?sslmode=disable",
			mustNotHave: "hunter2",
		},
		{
			name:        "postgresql URL form",
			dsn:         "postgresql://appuser:s3cr3t@localhost/infralens",
			mustNotHave: "s3cr3t",
		},
		{
			name:        "libpq keyword/value form",
			dsn:         "host=db user=app password=hunter2 dbname=infralens sslmode=disable",
			mustNotHave: "hunter2",
		},
		{
			name:        "libpq form with quoted password",
			dsn:         "host=db user=app password='hunter 2' dbname=infralens",
			mustNotHave: "hunter 2",
		},
		{
			name:      "sqlite file path is untouched",
			dsn:       "infralens.db?_journal_mode=WAL",
			wantExact: "infralens.db?_journal_mode=WAL",
		},
		{
			name:      "sqlite in-memory is untouched",
			dsn:       ":memory:",
			wantExact: ":memory:",
		},
		{
			name:      "empty is untouched",
			dsn:       "",
			wantExact: "",
		},
		{
			name:      "URL with no credentials is untouched",
			dsn:       "postgres://db.internal:5432/infralens",
			wantExact: "postgres://db.internal:5432/infralens",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactDSN(tc.dsn)

			if tc.wantExact != "" || tc.dsn == "" {
				if got != tc.wantExact {
					t.Errorf("RedactDSN(%q) = %q, want %q", tc.dsn, got, tc.wantExact)
				}
				return
			}

			if tc.mustNotHave != "" && contains(got, tc.mustNotHave) {
				t.Errorf("RedactDSN(%q) = %q, still contains the credential %q", tc.dsn, got, tc.mustNotHave)
			}
		})
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return substr == ""
}
