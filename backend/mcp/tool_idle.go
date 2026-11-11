package mcp

import (
	"context"
	"math"

	"github.com/Herenn/Infralens/backend/service"
)

// ---------------------------------------------------------------------------
// get_idle_spend
// ---------------------------------------------------------------------------

type getIdleSpendArgs struct {
	Limit             int     `json:"limit"`
	AssumeNodeMonthly float64 `json:"assume_node_monthly"`
}

const (
	idleDefaultLimit = 10
	idleMaxLimit     = 50
	// Caps on the secondary lists, so a large cluster cannot push the result
	// past maxResultBytes; totals always cover everything.
	idleMaxHeldBack    = 15
	idleMaxNotes       = 8
	idleMaxItemNotes   = 4
	idleMaxPeers       = 5
	maxAssumeMonthlyMC = 1_000_000
)

func getIdleSpendTool() Tool {
	return Tool{
		Name:  "get_idle_spend",
		Title: "Idle spend (beta)",
		Description: "Beta. Estimate what idle services cost: services nothing has talked to while InfraLens was watching (orphans, no inbound traffic, inbound stopped, only reached by other idle services, decommission candidates), " +
			"ranked by an attribution of configured node prices, each with its network evidence and separate idle and cost confidence, plus the services that looked idle but were held back and why. " +
			"Candidates to verify with the owning team - never a recommendation to delete. Costs are null unless prices are configured; assume_node_monthly gives a labelled what-if.",
		InputSchema: obj(nil, map[string]Schema{
			"limit": limitArg(idleDefaultLimit, idleMaxLimit),
			"assume_node_monthly": Schema{
				"type":             "number",
				"description":      "What-if monthly price for nodes no configured price covers, in the report's currency. Labelled \"assumed\" in the answer. Omit to use only configured prices.",
				"exclusiveMinimum": 0, "maximum": maxAssumeMonthlyMC,
			},
		}),
		Run: Typed(func(ctx context.Context, env *Env, in getIdleSpendArgs) (any, error) {
			if in.AssumeNodeMonthly < 0 || in.AssumeNodeMonthly > maxAssumeMonthlyMC || math.IsNaN(in.AssumeNodeMonthly) {
				return nil, invalidArgs("assume_node_monthly must be a positive amount up to %d", maxAssumeMonthlyMC)
			}
			limit := clamp(in.Limit, idleDefaultLimit, 1, idleMaxLimit)
			// The report is cached and shared between callers: read it, never
			// modify it. Everything below copies into fresh maps.
			r, err := env.Topology.GetIdleSpend(ctx, service.IdleSpendQuery{AssumeNodeMonthly: in.AssumeNodeMonthly, Limit: limit})
			if err != nil {
				return nil, err
			}

			items := make([]map[string]any, 0, len(r.Items))
			for i := range r.Items {
				it := &r.Items[i]
				ev := map[string]any{
					"quiet_seconds":        it.Evidence.QuietSeconds,
					"quiet_is_lower_bound": it.Evidence.QuietIsLowerBound,
					"unobserved_seconds":   it.Evidence.UnobservedSeconds,
					"live_inbound_edges":   it.Evidence.LiveInboundEdges,
					"live_outbound_edges":  it.Evidence.LiveOutboundEdges,
					"last_seen":            formatTime(it.Evidence.LastSeen),
				}
				if it.Evidence.LastInbound != nil {
					ev["last_inbound"] = formatTime(*it.Evidence.LastInbound)
				}
				if it.Evidence.FirstSeen != nil {
					ev["first_seen"] = formatTime(*it.Evidence.FirstSeen)
				}
				if p := capStrings(it.Evidence.OutboundPeers, idleMaxPeers); len(p) > 0 {
					ev["outbound_peers"] = p
				}
				if p := capStrings(it.Evidence.IgnoredCallers, idleMaxPeers); len(p) > 0 {
					ev["ignored_callers"] = p
				}
				if p := capStrings(it.Evidence.IdleCallers, idleMaxPeers); len(p) > 0 {
					ev["idle_callers"] = p
				}
				if n := capStrings(it.Evidence.Notes, idleMaxItemNotes); len(n) > 0 {
					ev["notes"] = n
				}
				item := map[string]any{
					"id":              it.ID,
					"name":            it.Name,
					"node":            it.Node,
					"type":            it.Type,
					"tech":            it.Tech,
					"category":        it.Category,
					"idle_confidence": it.IdleConfidence,
					"cost_confidence": it.CostConfidence,
					"in_graph":        it.InGraph,
					"evidence":        ev,
				}
				if it.Cost != nil {
					item["cost"] = map[string]any{
						"monthly":      it.Cost.Monthly,
						"annual":       it.Cost.Annual,
						"price_source": it.Cost.PriceSource,
						"share_method": it.Cost.ShareMethod,
						"explanation":  it.Cost.Explanation,
						"in_total":     it.InTotal,
					}
					if !it.InTotal {
						item["cost"].(map[string]any)["not_in_total_reason"] = it.NotInTotalReason
					}
				} else {
					item["cost"] = nil
				}
				id := it.ID
				clean := sanitizeValue(dropEmpty(item)).(map[string]any)
				clean["id"] = id // IDs round-trip unaltered; see viewOf
				items = append(items, clean)
			}

			held := make([]map[string]any, 0, idleMaxHeldBack)
			for i, ex := range r.Excluded {
				if i == idleMaxHeldBack {
					break
				}
				m := sanitizeValue(dropEmpty(map[string]any{
					"name": ex.Name, "node": ex.Node, "reason": ex.Reason, "detail": ex.Detail,
				})).(map[string]any)
				m["id"] = ex.ID
				held = append(held, m)
			}

			out := map[string]any{
				"beta":         r.Beta,
				"disclaimer":   r.Disclaimer,
				"generated_at": formatTime(r.GeneratedAt),
				"currency":     r.Currency,
				"pricing": sanitizeValue(map[string]any{
					"configured": r.Pricing.Configured,
					"source":     r.Pricing.Source,
					"warnings":   capStrings(r.Pricing.Warnings, idleMaxNotes),
				}),
				"evidence_window": map[string]any{
					"history_enabled":    r.Evidence.HistoryEnabled,
					"from":               formatTime(r.Evidence.From),
					"to":                 formatTime(r.Evidence.To),
					"observed_seconds":   r.Evidence.ObservedSeconds,
					"unobserved_seconds": r.Evidence.UnobservedSeconds,
					"warming_up":         r.Evidence.WarmingUp,
					"cluster_grouped":    r.Evidence.ClusterGrouped,
				},
				"totals": map[string]any{
					"monthly":               r.Totals.Monthly,
					"annual":                r.Totals.Annual,
					"services":              r.Totals.Services,
					"priced":                r.Totals.Priced,
					"unpriced":              r.Totals.Unpriced,
					"monthly_by_confidence": r.Totals.MonthlyByConfidence,
				},
				"items":           items,
				"items_returned":  len(items),
				"held_back":       held,
				"held_back_total": r.ExcludedCount,
				"notes":           sanitizeValue(capStrings(r.Notes, idleMaxNotes)),
			}
			if kept := fitList(out, "items", items); kept < len(items) {
				out["items_returned"] = kept
				out["items_note"] = "fewer items than the limit were returned to stay within the result size cap; totals still cover every service"
			}
			if r.ExcludedCount < len(r.Excluded) {
				out["held_back_total"] = len(r.Excluded)
			}
			if len(r.Excluded) > len(held) {
				out["held_back_note"] = "held_back lists the first entries only; held_back_total counts them all"
			}
			return out, nil
		}),
	}
}

// capStrings returns at most max of in, as []any so sanitizeValue (which
// walks decoded-JSON shapes) cleans every entry.
func capStrings(in []string, max int) []any {
	if len(in) > max {
		in = in[:max]
	}
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// dropEmpty removes empty strings and nil-but-not-cost values so list entries
// stay compact. "cost": nil is kept: it says "not priced".
func dropEmpty(m map[string]any) map[string]any {
	for k, v := range m {
		if k == "cost" {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}
