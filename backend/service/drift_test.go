package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/storage"
)

type recordingNotifier struct {
	mu      sync.Mutex
	reports []*DriftReport
}

func (n *recordingNotifier) NotifyDrift(_ context.Context, r *DriftReport) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reports = append(n.reports, r)
	return nil
}

// driftHarness drives a DriftDetector over a real (in-memory SQLite) history
// store with a controllable clock.
type driftHarness struct {
	t     *testing.T
	store storage.Store
	ts    *TopologyService
	clock time.Time
}

func newDriftHarness(t *testing.T, start time.Time) *driftHarness {
	t.Helper()
	ts, store := newHistoryTestService(t, true) // maxGap 5m
	return &driftHarness{t: t, store: store, ts: ts, clock: start.Truncate(time.Second)}
}

func (h *driftHarness) detector(cfg DriftConfig) *DriftDetector {
	h.t.Helper()
	d, err := h.ts.NewDriftDetector(cfg, &recordingNotifier{})
	if err != nil {
		h.t.Fatalf("creating detector: %v", err)
	}
	d.now = func() time.Time { return h.clock }
	if err := d.seed(context.Background()); err != nil {
		h.t.Fatalf("seeding detector: %v", err)
	}
	return d
}

// see records an observation of src->dst at the harness's current clock.
func (h *driftHarness) see(src, dst string, port uint16) {
	h.t.Helper()
	conn := &storage.Connection{
		ID:       src + "->" + dst,
		SourceID: src,
		TargetID: dst,
		Port:     port,
		Protocol: "tcp",
	}
	if err := h.store.History().RecordConnection(context.Background(), conn, h.clock, 5*time.Minute); err != nil {
		h.t.Fatalf("recording %s: %v", conn.ID, err)
	}
}

func (h *driftHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *driftHarness) check(d *DriftDetector) *DriftReport {
	h.t.Helper()
	r, err := d.check(context.Background())
	if err != nil {
		h.t.Fatalf("check: %v", err)
	}
	return r
}

func edgeIDs(edges []DriftEdge) []string {
	ids := make([]string, 0, len(edges))
	for _, e := range edges {
		ids = append(ids, e.ConnectionID)
	}
	return ids
}

func assertEdges(t *testing.T, what string, got []DriftEdge, want ...string) {
	t.Helper()
	gotIDs := edgeIDs(got)
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Errorf("%s: got %v, want %v", what, gotIDs, want)
	}
}

func TestDriftReportsNewDependencyOnce(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour) // past the 1h warmup

	d := h.detector(DriftConfig{Interval: 5 * time.Minute, Warmup: time.Hour})

	h.advance(time.Minute)
	h.see("api", "cache", 6379)
	h.advance(4 * time.Minute)

	r := h.check(d)
	assertEdges(t, "appeared", r.Appeared, "api->cache")
	assertEdges(t, "disappeared", r.Disappeared)

	// Still observed on the next pass: known now, not news again.
	h.see("api", "cache", 6379)
	h.advance(5 * time.Minute)
	if r := h.check(d); !r.Empty() {
		t.Errorf("second pass should be empty, got appeared=%v disappeared=%v", edgeIDs(r.Appeared), edgeIDs(r.Disappeared))
	}
}

// TestDriftWarmupOnFreshInstall: on day one every edge is new. Announcing
// the whole cluster as drift would train people to ignore the channel.
func TestDriftWarmupOnFreshInstall(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	d := h.detector(DriftConfig{Warmup: time.Hour}) // empty history: warmup runs from now

	h.advance(10 * time.Minute)
	h.see("api", "db", 5432)
	h.advance(5 * time.Minute)
	if r := h.check(d); !r.Empty() {
		t.Fatalf("edge first seen during warmup must not be reported, got %v", edgeIDs(r.Appeared))
	}

	h.advance(time.Hour)
	h.see("api", "queue", 5672)
	h.advance(5 * time.Minute)
	assertEdges(t, "appeared after warmup", h.check(d).Appeared, "api->queue")
}

func TestDriftReportsDisappearanceOnceAndRearms(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.see("api", "legacy", 8080)

	d := h.detector(DriftConfig{GoneAfter: 2 * time.Hour, Warmup: 0})

	// api->db keeps being observed; api->legacy goes quiet.
	for i := 0; i < 6; i++ {
		h.advance(30 * time.Minute)
		h.see("api", "db", 5432)
	}
	r := h.check(d)
	assertEdges(t, "disappeared", r.Disappeared, "api->legacy")
	assertEdges(t, "appeared", r.Appeared)

	h.advance(30 * time.Minute)
	h.see("api", "db", 5432)
	if r := h.check(d); len(r.Disappeared) != 0 {
		t.Errorf("disappearance must be reported once, got again: %v", edgeIDs(r.Disappeared))
	}

	// It comes back, then goes away again: that is a second disappearance.
	h.see("api", "legacy", 8080)
	h.advance(5 * time.Minute)
	if r := h.check(d); !r.Empty() {
		t.Errorf("a known edge returning is not new: %v", edgeIDs(r.Appeared))
	}
	for i := 0; i < 6; i++ {
		h.advance(30 * time.Minute)
		h.see("api", "db", 5432)
	}
	assertEdges(t, "second disappearance", h.check(d).Disappeared, "api->legacy")
}

// TestDriftSilentWhenCollectionStops: if every agent stops reporting, that is
// a collection outage, not the whole architecture disappearing.
func TestDriftSilentWhenCollectionStops(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.see("api", "cache", 6379)

	d := h.detector(DriftConfig{GoneAfter: 2 * time.Hour, Warmup: 0})

	h.advance(48 * time.Hour)
	if r := h.check(d); !r.Empty() {
		t.Errorf("nothing observed at all must not read as everything gone, got %v", edgeIDs(r.Disappeared))
	}
}

// TestDriftRestartDoesNotReannounce: the baseline is reloaded from history,
// so a backend restart is not a flood of "new" dependencies.
func TestDriftRestartDoesNotReannounce(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	h.see("api", "db", 5432)
	h.see("api", "cache", 6379)

	h.advance(time.Minute)
	d := h.detector(DriftConfig{Warmup: time.Hour}) // "restarted" process
	h.advance(5 * time.Minute)
	h.see("api", "db", 5432)
	if r := h.check(d); !r.Empty() {
		t.Errorf("restart re-announced known edges: appeared=%v disappeared=%v", edgeIDs(r.Appeared), edgeIDs(r.Disappeared))
	}
}

func TestDriftExternalDependencies(t *testing.T) {
	h := newDriftHarness(t, time.Now().Add(-72*time.Hour))
	h.see("api", "db", 5432)
	h.advance(2 * time.Hour)
	d := h.detector(DriftConfig{Warmup: time.Hour})

	h.see("api", "93.184.216.34:443", 443)    // outbound to the internet
	h.see("api", "worker", 9000)              // new internal edge
	h.see("8.8.8.8", "10.0.0.5/nginx", 443)   // inbound internet client: churn, not drift
	h.see("100.64.1.2", "10.0.0.5/nginx", 80) // CGNAT/overlay range: internal
	h.advance(5 * time.Minute)

	r := h.check(d)
	assertEdges(t, "appeared (external first)", r.Appeared, "api->93.184.216.34:443", "100.64.1.2->10.0.0.5/nginx", "api->worker")
	if !r.Appeared[0].External {
		t.Error("internet destination not flagged external")
	}
	for _, e := range r.Appeared[1:] {
		if e.External {
			t.Errorf("%s wrongly flagged external", e.ConnectionID)
		}
	}
}

func TestNewDriftDetectorRequiresHistory(t *testing.T) {
	ts, _ := newHistoryTestService(t, false)
	if _, err := ts.NewDriftDetector(DriftConfig{}, &recordingNotifier{}); err != ErrHistoryDisabled {
		t.Errorf("expected ErrHistoryDisabled, got %v", err)
	}
}

func TestIsPublicServiceID(t *testing.T) {
	cases := map[string]bool{
		"93.184.216.34":         true,
		"93.184.216.34:443":     true,
		"93.184.216.34/nginx":   true,
		"[2606:4700::1111]:443": true,
		"2606:4700::1111":       true,
		"10.0.0.1":              false,
		"10.0.0.1:5432":         false,
		"192.168.1.5/postgres":  false,
		"172.16.0.9:80":         false,
		"127.0.0.1:8080":        false,
		"169.254.169.254:80":    false,
		"100.64.0.1":            false,
		"::1":                   false,
		"fd00::1":               false,
		"Deploy: default/api":   false,
		"default/api":           false,
		"":                      false,
	}
	for id, want := range cases {
		if got := isPublicServiceID(id); got != want {
			t.Errorf("isPublicServiceID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestWebhookDriftNotifier(t *testing.T) {
	var got driftWebhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding payload: %v", err)
		}
	}))
	defer srv.Close()

	report := &DriftReport{
		DetectedAt: time.Now(),
		Appeared: []DriftEdge{
			{ConnectionID: "api->1.1.1.1:443", SourceID: "api", TargetID: "1.1.1.1:443", Port: 443, External: true},
		},
		Disappeared: []DriftEdge{
			{ConnectionID: "api->legacy", SourceID: "api", TargetID: "legacy", Port: 8080, Protocol: "tcp"},
		},
	}
	if err := NewWebhookDriftNotifier(srv.URL).NotifyDrift(context.Background(), report); err != nil {
		t.Fatalf("NotifyDrift: %v", err)
	}

	if got.Event != "topology.drift" || got.AppearedTotal != 1 || got.DisappearedTotal != 1 {
		t.Errorf("unexpected payload: %+v", got)
	}
	for _, want := range []string{"1 new dependency (1 to the public internet)", "1 dependency no longer observed", "api -> 1.1.1.1:443 (port 443/tcp) [internet]", "api -> legacy"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("text missing %q:\n%s", want, got.Text)
		}
	}
}

func TestWebhookDriftNotifierErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	report := &DriftReport{Appeared: []DriftEdge{{ConnectionID: "a->b"}}}
	if err := NewWebhookDriftNotifier(srv.URL+"/secret-token").NotifyDrift(context.Background(), report); err == nil {
		t.Error("expected an error for HTTP 500")
	} else if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks the webhook URL: %v", err)
	}

	// Unreachable: the transport error must not leak the URL either.
	err := NewWebhookDriftNotifier("http://127.0.0.1:1/secret-token").NotifyDrift(context.Background(), report)
	if err == nil {
		t.Fatal("expected an error for an unreachable webhook")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks the webhook URL: %v", err)
	}
}

func TestDriftPayloadCapsLists(t *testing.T) {
	report := &DriftReport{}
	for i := 0; i < maxDriftItems+5; i++ {
		report.Appeared = append(report.Appeared, DriftEdge{ConnectionID: "x", SourceID: "a", TargetID: "b"})
	}
	p := buildDriftWebhookPayload(report)
	if len(p.Appeared) != maxDriftItems || p.AppearedTotal != maxDriftItems+5 {
		t.Errorf("got %d listed / %d total, want %d / %d", len(p.Appeared), p.AppearedTotal, maxDriftItems, maxDriftItems+5)
	}
	if p.Disappeared == nil {
		t.Error("empty list should encode as [], not null")
	}
	if !strings.Contains(p.Text, "...and 20 more") {
		t.Errorf("text should truncate long lists:\n%s", p.Text)
	}
}
