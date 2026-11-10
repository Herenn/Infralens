package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/Herenn/Infralens/backend/service"
)

// maxAssumedNodeMonthly bounds the what-if price so a stray extra digit in a
// query string cannot produce a headline in the billions.
const maxAssumedNodeMonthly = 1_000_000

// HandleGetIdleCost returns idle services, the network evidence that they are
// idle, and an approximate monthly cost for each (see service.GetIdleSpend).
//
// Query parameters:
//
//	assume_node_monthly=<amount>  what-if price for nodes nothing configured
//	                              prices; labelled "assumed" in the response
//	limit=<n>                     cap on itemized services (default 100);
//	                              totals always cover everything
//	format=json|markdown|csv      default json
//
// Auth: a public read like /graph/orphans when no UI_PASSWORD is set (it is
// on the v3.0 auth skip list, so the bundled UI works with only API_KEY set);
// with UI_PASSWORD it needs a session or the API key, like every route.
func (h *TopologyHandler) HandleGetIdleCost(w http.ResponseWriter, r *http.Request) {
	q := service.IdleSpendQuery{}

	if raw := r.URL.Query().Get("assume_node_monthly"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > maxAssumedNodeMonthly {
			http.Error(w, "Invalid 'assume_node_monthly' parameter: expected a positive amount", http.StatusBadRequest)
			return
		}
		q.AssumeNodeMonthly = v
	}

	limit, ok := parseLimit(r, 0)
	if !ok {
		http.Error(w, "Invalid 'limit' parameter: expected a positive integer", http.StatusBadRequest)
		return
	}
	q.Limit = limit

	format := r.URL.Query().Get("format")
	switch format {
	case "", "json", "markdown", "md", "csv":
	default:
		http.Error(w, "Invalid 'format' parameter: expected json, markdown or csv", http.StatusBadRequest)
		return
	}

	report, err := h.topology.GetIdleSpend(r.Context(), q)
	if err != nil {
		http.Error(w, "Failed to estimate idle spend", http.StatusInternalServerError)
		return
	}

	stamp := report.GeneratedAt.UTC().Format("2006-01-02")
	switch format {
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(service.RenderIdleSpendMarkdown(report)))
	case "csv":
		body, err := service.RenderIdleSpendCSV(report)
		if err != nil {
			http.Error(w, "Failed to render CSV", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="infralens-idle-spend-`+stamp+`.csv"`)
		_, _ = w.Write(body)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	}
}
