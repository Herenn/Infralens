package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Herenn/Infralens/backend/config"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// newTestServer builds the real server - router, middleware chain, auth -
// from environment-driven config, over an in-memory store.
func newTestServer(t *testing.T, env map[string]string) http.Handler {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg := config.Load()
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := NewServer(cfg, store, nil, nil)
	t.Cleanup(srv.Close)
	return srv.Handler()
}

func status(h http.Handler, method, path string) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code
}

// With an API key configured (the default production setup) the bundled UI
// sends no key. The drift feed must stay readable, like the other topology
// reads - its data is already on the public WebSocket - while ingestion
// still requires the key.
func TestDriftFeedReadableWithAPIKeySet(t *testing.T) {
	h := newTestServer(t, map[string]string{"API_KEY": "s3cret", "ALLOW_NO_AUTH": "", "HISTORY_ENABLED": "true", "DRIFT_ENABLED": ""})

	for _, path := range []string{"/api/v1/drift/events", "/api/v1/drift/summary"} {
		if got := status(h, "GET", path); got != http.StatusOK {
			t.Errorf("GET %s without a key: %d, want 200", path, got)
		}
	}
	if got := status(h, "POST", "/api/v1/events"); got != http.StatusUnauthorized {
		t.Errorf("POST /api/v1/events without a key: %d, want 401 (ingestion must stay protected)", got)
	}
}

// DRIFT_ENABLED=false removes the feature: the endpoints do not exist, which
// is what the UI takes as "hide the Changes button".
func TestDriftFeedAbsentWhenDisabled(t *testing.T) {
	h := newTestServer(t, map[string]string{"API_KEY": "", "ALLOW_NO_AUTH": "true", "HISTORY_ENABLED": "true", "DRIFT_ENABLED": "false"})
	for _, path := range []string{"/api/v1/drift/events", "/api/v1/drift/summary"} {
		if got := status(h, "GET", path); got != http.StatusNotFound {
			t.Errorf("GET %s with DRIFT_ENABLED=false: %d, want 404", path, got)
		}
	}
}
