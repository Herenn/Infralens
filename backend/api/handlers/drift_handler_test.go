package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/storage"
)

// withDriftRoutes registers the drift endpoints the way router.go does.
func withDriftRoutes(ts *testServer) *testServer {
	h := handlers.NewDriftHandler(ts.topology)
	ts.router.HandleFunc("/api/v1/drift/events", h.HandleListEvents).Methods("GET")
	ts.router.HandleFunc("/api/v1/drift/summary", h.HandleSummary).Methods("GET")
	return ts
}

// seedDriftEvents stores n events a minute apart ending `newest`; odd ones
// disappeared, every third internet-bound.
func seedDriftEvents(t *testing.T, ts *testServer, newest time.Time, n int) []storage.DriftEvent {
	t.Helper()
	var events []storage.DriftEvent
	for i := 0; i < n; i++ {
		at := newest.Add(-time.Duration(n-1-i) * time.Minute)
		kind := storage.DriftAppeared
		if i%2 == 1 {
			kind = storage.DriftDisappeared
		}
		events = append(events, storage.DriftEvent{
			DetectedAt:   at,
			Kind:         kind,
			ConnectionID: fmt.Sprintf("svc-%d->dst-%d", i, i),
			SourceID:     fmt.Sprintf("svc-%d", i),
			SourceName:   fmt.Sprintf("service %d", i),
			TargetID:     fmt.Sprintf("dst-%d", i),
			Port:         443,
			External:     i%3 == 0,
			FirstSeen:    at.Add(-2 * time.Minute),
			LastSeen:     at.Add(-time.Minute),
			Fingerprint:  fmt.Sprintf("handler-%d", i),
		})
	}
	inserted, err := ts.topology.ImportDriftEvents(t.Context(), events)
	if err != nil {
		t.Fatalf("seeding drift events: %v", err)
	}
	return inserted
}

func getDriftEvents(t *testing.T, ts *testServer, query string) (int, handlers.DriftEventsResponse, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/drift/events"+query, nil)
	w := httptest.NewRecorder()
	ts.router.ServeHTTP(w, req)
	var resp handlers.DriftEventsResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		return w.Code, resp, ""
	}
	return w.Code, resp, w.Body.String()
}

func TestHandleDriftEvents_NewestFirstWithPagination(t *testing.T) {
	ts := withDriftRoutes(newTestServerWithHistory(t))
	newest := time.Now().Add(-time.Minute)
	seedDriftEvents(t, ts, newest, 7)

	code, page, body := getDriftEvents(t, ts, "?limit=3")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	if len(page.Events) != 3 || page.NextCursor == "" {
		t.Fatalf("first page: %d events, cursor %q", len(page.Events), page.NextCursor)
	}
	first := page.Events[0]
	if first.ConnectionID != "svc-6->dst-6" || first.Kind != "appeared" || first.SourceName != "service 6" ||
		first.Protocol != "tcp" || !first.External || first.Port != 443 {
		t.Errorf("unexpected newest event: %+v", first)
	}
	// Stored times have microsecond precision; the clock may be finer (Linux).
	if at, err := time.Parse(time.RFC3339Nano, first.DetectedAt); err != nil || !at.Equal(newest.Truncate(time.Microsecond)) {
		t.Errorf("detected_at %q does not round-trip to %s (err %v)", first.DetectedAt, newest, err)
	}

	var all []string
	cursor := ""
	for i := 0; i < 5; i++ {
		q := "?limit=3"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		_, page, _ := getDriftEvents(t, ts, q)
		for _, ev := range page.Events {
			all = append(all, ev.ConnectionID)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(all) != 7 || all[0] != "svc-6->dst-6" || all[6] != "svc-0->dst-0" {
		t.Errorf("paged listing: %v", all)
	}
}

func TestHandleDriftEvents_Filters(t *testing.T) {
	ts := withDriftRoutes(newTestServerWithHistory(t))
	newest := time.Now().Add(-time.Minute)
	events := seedDriftEvents(t, ts, newest, 7)

	_, gone, _ := getDriftEvents(t, ts, "?kind=disappeared")
	if len(gone.Events) != 3 {
		t.Errorf("kind=disappeared: %d events, want 3", len(gone.Events))
	}
	_, ext, _ := getDriftEvents(t, ts, "?external=true")
	if len(ext.Events) != 3 { // 0, 3, 6
		t.Errorf("external=true: %d events, want 3", len(ext.Events))
	}
	_, both, _ := getDriftEvents(t, ts, "?external=true&kind=appeared")
	if len(both.Events) != 2 { // 0, 6
		t.Errorf("external=true&kind=appeared: %d events, want 2", len(both.Events))
	}

	// since is exclusive: passing back the newest detected_at returns nothing,
	// which is what an unread counter relies on.
	_, latest, _ := getDriftEvents(t, ts, "?limit=1")
	_, newer, _ := getDriftEvents(t, ts, "?since="+url.QueryEscape(latest.Events[0].DetectedAt))
	if len(newer.Events) != 0 {
		t.Errorf("since=<newest detected_at> returned %d events, want 0", len(newer.Events))
	}
	since := events[3].DetectedAt.Format(time.RFC3339Nano)
	until := events[6].DetectedAt.Format(time.RFC3339Nano)
	_, window, _ := getDriftEvents(t, ts, "?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until))
	if len(window.Events) != 2 || window.Events[0].ConnectionID != "svc-5->dst-5" {
		t.Errorf("(since, until) window: %+v", window.Events)
	}
}

func TestHandleDriftEvents_BadParams(t *testing.T) {
	ts := withDriftRoutes(newTestServerWithHistory(t))
	for _, q := range []string{
		"?kind=moved",
		"?external=maybe",
		"?limit=0",
		"?limit=abc",
		"?since=yesterday",
		"?until=2026-13-01T00:00:00Z",
		"?since=2026-11-02T00:00:00Z&until=2026-11-01T00:00:00Z",
		"?cursor=not-a-cursor!",
	} {
		if code, _, _ := getDriftEvents(t, ts, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}

func TestHandleDriftEvents_HistoryDisabled(t *testing.T) {
	ts := withDriftRoutes(newTestServer(t))
	for _, path := range []string{"/api/v1/drift/events", "/api/v1/drift/summary"} {
		w := httptest.NewRecorder()
		ts.router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s with history off: status %d, want 400", path, w.Code)
		}
	}
}

func TestHandleDriftSummary(t *testing.T) {
	ts := withDriftRoutes(newTestServerWithHistory(t))
	newest := time.Now().Add(-time.Minute)
	events := seedDriftEvents(t, ts, newest, 7)

	get := func(q string) handlers.DriftSummaryResponse {
		t.Helper()
		w := httptest.NewRecorder()
		ts.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/drift/summary"+q, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("summary%s: status %d: %s", q, w.Code, w.Body.String())
		}
		var resp handlers.DriftSummaryResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	all := get("")
	if all.Total != 7 || all.Appeared != 4 || all.Disappeared != 3 || all.External != 3 || all.Latest == nil {
		t.Errorf("unexpected summary: %+v", all)
	}
	if all.Detector.Running {
		t.Error("no detector is running in this test, but the summary says one is")
	}
	if all.Detector.SilentAgents == nil || len(all.Detector.SilentAgents) != 0 || all.Detector.SilentAgentsTotal != 0 {
		t.Errorf("silent_agents = %v (total %d), want an empty list", all.Detector.SilentAgents, all.Detector.SilentAgentsTotal)
	}

	unread := get("?since=" + url.QueryEscape(events[4].DetectedAt.UTC().Format(time.RFC3339Nano)))
	if unread.Total != 2 {
		t.Errorf("events after #4: %d, want 2", unread.Total)
	}
	none := get("?since=" + url.QueryEscape(*all.Latest))
	if none.Total != 0 || none.Latest != nil {
		t.Errorf("events after the latest: %+v", none)
	}
}
