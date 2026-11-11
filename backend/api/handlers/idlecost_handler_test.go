package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/Herenn/Infralens/backend/api/handlers"
	"github.com/Herenn/Infralens/backend/api/middleware"
	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
	"github.com/Herenn/Infralens/backend/storage/sqlite"
)

// newIdleCostServer wires the idle-cost route over an in-memory store holding
// one idle server (reports: talks to postgres, nothing talks to it) on a
// node shared with a busy gateway, with 14 days of backfilled history.
func newIdleCostServer(t *testing.T, cfg *service.IdleCostConfig) (*mux.Router, *service.TopologyService) {
	t.Helper()
	store, err := sqlite.New(storage.Config{Driver: "sqlite", DSN: ":memory:", AutoMigrate: true, HistoryEnabled: true})
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	bus := service.NewEventBus(16)
	t.Cleanup(func() { bus.Close(); _ = store.Close() })

	ts := service.NewTopologyService(store, bus)
	ts.EnableHistory(5*time.Minute, storage.DefaultHistoryRetention)
	if cfg != nil {
		ts.ConfigureIdleCost(*cfg)
	}

	ctx := context.Background()
	now := time.Now()
	services := []storage.Service{
		{ID: "1.2.3.4", Name: "external", Tech: "External"},
		{ID: "10.0.0.1/gw", Name: "gateway", Type: "web_server", Node: "n1"},
		{ID: "10.0.0.2:5432", Name: "postgres", Type: "database", Node: "n1"},
		{ID: "10.0.0.3/reports", Name: "reports", Type: "web_server", Node: "n1"},
	}
	for i := range services {
		if err := ts.ImportHistory(ctx, []service.HistorySpan{{Service: &services[i], From: now.Add(-14 * 24 * time.Hour), To: now}}); err != nil {
			t.Fatal(err)
		}
		svc := services[i]
		if err := ts.AddOrUpdateService(ctx, &svc); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []storage.Connection{
		{ID: "1.2.3.4->10.0.0.1/gw:443", SourceID: "1.2.3.4", TargetID: "10.0.0.1/gw", Port: 443},
		{ID: "10.0.0.1/gw->10.0.0.2:5432:5432", SourceID: "10.0.0.1/gw", TargetID: "10.0.0.2:5432", Port: 5432},
		{ID: "10.0.0.3/reports->10.0.0.2:5432:5432", SourceID: "10.0.0.3/reports", TargetID: "10.0.0.2:5432", Port: 5432},
	} {
		c := c
		c.LastSeen = now
		if err := ts.ImportHistory(ctx, []service.HistorySpan{{Connection: &c, From: now.Add(-14 * 24 * time.Hour), To: now}}); err != nil {
			t.Fatal(err)
		}
		if err := ts.AddConnection(ctx, &c); err != nil {
			t.Fatal(err)
		}
	}

	r := mux.NewRouter()
	r.HandleFunc("/api/v1/graph/idle-cost", handlers.NewTopologyHandler(ts).HandleGetIdleCost).Methods("GET")
	return r, ts
}

func getIdleCost(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/graph/idle-cost"+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIdleCostEndpointUnconfigured(t *testing.T) {
	r, _ := newIdleCostServer(t, nil)
	rec := getIdleCost(t, r, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var report service.IdleSpendReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if report.Pricing.Source != "none" || report.Pricing.Configured {
		t.Errorf("pricing = %+v", report.Pricing)
	}
	if len(report.Items) != 1 || report.Items[0].ID != "10.0.0.3/reports" {
		t.Fatalf("items = %+v (excluded %+v, notes %v)", report.Items, report.Excluded, report.Notes)
	}
	if report.Items[0].Cost != nil {
		t.Errorf("unconfigured must not invent a cost: %+v", report.Items[0].Cost)
	}
	if report.Items[0].Category != service.IdleCategoryNoInbound {
		t.Errorf("category = %s", report.Items[0].Category)
	}
	// Arrays are always present (never null) so clients can iterate blindly.
	for _, key := range []string{`"items":[`, `"excluded":[`, `"nodes":[`, `"notes":[`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("response missing %s", key)
		}
	}
}

func TestIdleCostEndpointAssumedAndConfigured(t *testing.T) {
	r, _ := newIdleCostServer(t, nil)
	rec := getIdleCost(t, r, "?assume_node_monthly=150")
	var report service.IdleSpendReport
	_ = json.Unmarshal(rec.Body.Bytes(), &report)
	if report.Pricing.Source != "assumed" || report.Totals.Monthly != 50 {
		t.Errorf("assumed: source=%s monthly=%v", report.Pricing.Source, report.Totals.Monthly)
	}

	r2, _ := newIdleCostServer(t, &service.IdleCostConfig{DefaultNodeMonthly: 300, Currency: "EUR", Warmup: -1})
	rec = getIdleCost(t, r2, "")
	report = service.IdleSpendReport{}
	_ = json.Unmarshal(rec.Body.Bytes(), &report)
	if report.Totals.Monthly != 100 || report.Totals.Annual != 1200 || report.CurrencySymbol != "€" {
		t.Errorf("configured: %+v symbol=%q", report.Totals, report.CurrencySymbol)
	}
}

func TestIdleCostEndpointFormats(t *testing.T) {
	r, _ := newIdleCostServer(t, &service.IdleCostConfig{DefaultNodeMonthly: 300, Warmup: -1})

	md := getIdleCost(t, r, "?format=markdown")
	if md.Code != http.StatusOK || !strings.HasPrefix(md.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("markdown: %d %s", md.Code, md.Header().Get("Content-Type"))
	}
	if !strings.Contains(md.Body.String(), "~$100/month") || !strings.Contains(md.Body.String(), "**reports**") {
		t.Errorf("markdown body:\n%s", md.Body.String())
	}

	csv := getIdleCost(t, r, "?format=csv")
	if csv.Code != http.StatusOK || !strings.HasPrefix(csv.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("csv: %d %s", csv.Code, csv.Header().Get("Content-Type"))
	}
	if !strings.Contains(csv.Header().Get("Content-Disposition"), "infralens-idle-spend-") {
		t.Errorf("csv disposition = %q", csv.Header().Get("Content-Disposition"))
	}
	if lines := strings.Count(strings.TrimSpace(csv.Body.String()), "\n"); lines != 1 {
		t.Errorf("csv should be header + 1 row, got %d newlines:\n%s", lines, csv.Body.String())
	}
}

func TestIdleCostEndpointRejectsBadParams(t *testing.T) {
	r, _ := newIdleCostServer(t, nil)
	for _, q := range []string{
		"?assume_node_monthly=abc", "?assume_node_monthly=-5", "?assume_node_monthly=0",
		"?assume_node_monthly=NaN", "?assume_node_monthly=1e12", "?limit=0", "?limit=x", "?format=xml",
	} {
		if rec := getIdleCost(t, r, q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

// PM decision: with no UI password, idle-cost is a public read like orphans
// and criticality (it is cached server-side, so this is safe to expose).
// Verified through the real middleware, which must still protect ingest.
func TestIdleCostEndpointIsPublicReadUnderAPIKeyAuth(t *testing.T) {
	r, _ := newIdleCostServer(t, nil)
	r.HandleFunc("/api/v1/events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }).Methods("POST")
	cfg := middleware.DefaultAuthConfig()
	cfg.APIKey = "secret"
	h := middleware.Auth(cfg)(r)

	if rec := getIdleCost(t, h, ""); rec.Code != http.StatusOK {
		t.Errorf("idle-cost without key: status = %d, want 200", rec.Code)
	}
	if rec := getIdleCost(t, h, "?format=csv"); rec.Code != http.StatusOK {
		t.Errorf("idle-cost csv without key: status = %d, want 200", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("ingest without key: status = %d, want 401 (skip entry must be exact)", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/graph/idle-cost-admin", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("neighbouring path without key: status = %d, want 401", rec.Code)
	}
}
