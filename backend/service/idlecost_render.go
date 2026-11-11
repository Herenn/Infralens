package service

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Renderers for the idle-spend report, shared by the HTTP endpoint
// (?format=markdown|csv), the UI's copy/export buttons, and anything else that
// wants a cleanup-ticket-ready artifact. Kept beside the estimator so the
// wording of method and confidence stays in one place.

var idleCategoryLabels = map[string]string{
	IdleCategoryOrphan:          "Orphan (no connections)",
	IdleCategoryNoInbound:       "Nothing connects to it",
	IdleCategoryQuiet:           "Quiet (inbound stopped)",
	IdleCategoryIdleCallersOnly: "Only idle services call it",
	IdleCategoryStale:           "Not seen (decommission candidate)",
}

// IdleCategoryLabel is the human label for a category.
func IdleCategoryLabel(c string) string {
	if l, ok := idleCategoryLabels[c]; ok {
		return l
	}
	return c
}

// RenderIdleSpendMarkdown renders the report as a Markdown cleanup ticket.
//
// Every interpolated value is escaped with md(): service, node and peer names
// come from observed process and workload names, which whoever runs a process
// controls, and a ticket renderer would otherwise turn a name like
// "![x](http://host/pixel.png)" into a tracking image or a link.
func RenderIdleSpendMarkdown(r *IdleSpendReport) string {
	var b strings.Builder
	sym := md(r.CurrencySymbol)

	b.WriteString("## Idle spend (beta) - InfraLens\n\n")
	b.WriteString("> " + md(IdleDisclaimer) + "\n\n")
	fmt.Fprintf(&b, "_Generated %s._ ", r.GeneratedAt.UTC().Format("2006-01-02 15:04 MST"))
	if r.Evidence.HistoryEnabled {
		fmt.Fprintf(&b, "Evidence: %s of observed network history", humanDuration(time.Duration(r.Evidence.ObservedSeconds)*time.Second))
		if r.Evidence.UnobservedSeconds > 3600 {
			fmt.Fprintf(&b, " (%s not observed, not counted as silence)", humanDuration(time.Duration(r.Evidence.UnobservedSeconds)*time.Second))
		}
		b.WriteString(".\n\n")
	} else {
		b.WriteString("Evidence: current window only (history disabled).\n\n")
	}

	if r.Totals.Priced > 0 {
		fmt.Fprintf(&b, "**~%s%s/month (~%s%s/year) across %d candidate%s**",
			sym, md(formatMoney(math.Round(r.Totals.Monthly))), sym, md(formatMoney(math.Round(r.Totals.Annual))),
			r.Totals.Services, idlePlural(r.Totals.Services))
		if r.Totals.Unpriced > 0 {
			fmt.Fprintf(&b, " (%d unpriced)", r.Totals.Unpriced)
		}
		b.WriteString("\n\n")
		if r.Totals.NotInTotalMonthly > 0 {
			fmt.Fprintf(&b, "Not in the total: %s%s/month", sym, md(formatMoney(r.Totals.NotInTotalMonthly)))
			var why []string
			for _, reason := range sortedReasons(r.Totals.NotInTotalByReason) {
				why = append(why, fmt.Sprintf("%s: %s%s", md(NotInTotalReasonText(reason)), sym, md(formatMoney(r.Totals.NotInTotalByReason[reason]))))
			}
			if len(why) > 0 {
				b.WriteString(" (" + strings.Join(why, "; ") + ")")
			}
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "By confidence: high %s%s · medium %s%s · low %s%s\n\n",
			sym, md(formatMoney(r.Totals.MonthlyByConfidence[ConfidenceHigh])),
			sym, md(formatMoney(r.Totals.MonthlyByConfidence[ConfidenceMedium])),
			sym, md(formatMoney(r.Totals.MonthlyByConfidence[ConfidenceLow])))
	} else {
		fmt.Fprintf(&b, "**%d candidate%s** (no prices configured - costs not estimated)\n\n", r.Totals.Services, idlePlural(r.Totals.Services))
	}
	b.WriteString(md(pricingLine(r)) + "\n\n")
	for _, n := range r.Notes {
		b.WriteString("- " + md(n) + "\n")
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
	}

	if len(r.Items) == 0 {
		b.WriteString("Nothing idle found.\n")
	} else {
		fmt.Fprintf(&b, "| # | Service | Node | Category | Evidence | Est. %s/mo | Confidence |\n", md(r.Currency))
		b.WriteString("|---|---|---|---|---|---:|---|\n")
		for i, it := range r.Items {
			cost := "-"
			if it.Cost != nil {
				cost = sym + md(formatMoney(it.Cost.Monthly))
				if !it.InTotal {
					cost += " (not in total: " + md(NotInTotalReasonText(it.NotInTotalReason)) + ")"
				}
			}
			why := IdleCategoryLabel(it.Category)
			if len(it.Evidence.Notes) > 0 {
				why = it.Evidence.Notes[0]
			}
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n",
				i+1, md(it.Name), md(orDash(it.Node)), md(IdleCategoryLabel(it.Category)), md(why), cost, md(it.Confidence))
		}
		b.WriteString("\n### Evidence\n\n")
		for _, it := range r.Items {
			fmt.Fprintf(&b, "- **%s** (%s)\n", md(it.Name), md(it.ID))
			for _, n := range it.Evidence.Notes {
				fmt.Fprintf(&b, "  - %s\n", md(n))
			}
			if it.Cost != nil {
				fmt.Fprintf(&b, "  - Cost: %s (cost confidence %s)\n", md(it.Cost.Explanation), md(it.CostConfidence))
			}
		}
	}

	if r.ExcludedCount > 0 {
		fmt.Fprintf(&b, "\n### Held back (%d)\n\n", r.ExcludedCount)
		for _, e := range r.Excluded {
			fmt.Fprintf(&b, "- %s - %s\n", md(e.Name), md(e.Detail))
		}
	}
	return b.String()
}

// md escapes text for inline Markdown: every ASCII punctuation character
// that has meaning anywhere in CommonMark/GFM is backslash-escaped (which
// CommonMark defines as always yielding the literal character), and line
// breaks are flattened so a value cannot start a new block or table row.
func md(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		case '\\', '`', '*', '_', '{', '}', '[', ']', '(', ')', '#', '+', '-', '.', '!', '|', '<', '>', '~', '&', '"', '\'', '=', ':':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RenderIdleSpendCSV renders one row per idle service.
func RenderIdleSpendCSV(r *IdleSpendReport) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{
		"id", "name", "node", "category", "quiet_days", "quiet_is_lower_bound", "last_inbound", "last_seen",
		"monthly_cost", "annual_cost", "currency", "price_source", "share_method", "share",
		"in_total", "not_in_total_reason",
		"idle_confidence", "cost_confidence", "confidence", "evidence",
	})
	for _, it := range r.Items {
		monthly, annual, source, method, share := "", "", "", "", ""
		if it.Cost != nil {
			monthly = strconv.FormatFloat(it.Cost.Monthly, 'f', 2, 64)
			annual = strconv.FormatFloat(it.Cost.Annual, 'f', 2, 64)
			source = it.Cost.PriceSource
			method = it.Cost.ShareMethod
			share = strconv.FormatFloat(it.Cost.Share, 'f', 4, 64)
		}
		lastInbound := ""
		if it.Evidence.LastInbound != nil {
			lastInbound = it.Evidence.LastInbound.UTC().Format(time.RFC3339)
		}
		_ = w.Write([]string{
			csvSafe(it.ID), csvSafe(it.Name), csvSafe(it.Node), it.Category,
			strconv.FormatFloat(float64(it.Evidence.QuietSeconds)/86400, 'f', 1, 64),
			strconv.FormatBool(it.Evidence.QuietIsLowerBound),
			lastInbound, it.Evidence.LastSeen.UTC().Format(time.RFC3339),
			monthly, annual, r.Currency, source, method, share,
			strconv.FormatBool(it.InTotal), it.NotInTotalReason,
			it.IdleConfidence, it.CostConfidence, it.Confidence,
			csvSafe(strings.Join(it.Evidence.Notes, " ")),
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// NotInTotalReasonText words a not_in_total_reason for people.
func NotInTotalReasonText(reason string) string {
	switch reason {
	case NotInTotalCurrentWindow:
		return "current-window evidence only"
	case NotInTotalSilentAgent:
		return "an agent in its group was not reporting"
	default:
		return "see the notes"
	}
}

func sortedReasons(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pricingLine(r *IdleSpendReport) string {
	sym := r.CurrencySymbol
	p := r.Pricing
	var parts []string
	switch p.Source {
	case "demo":
		parts = append(parts, "Prices: demo values (illustrative only)")
	case "assumed":
		parts = append(parts, "Prices: assumed for this view")
	case "none":
		parts = append(parts, "Prices: not configured")
	default:
		parts = append(parts, "Prices: configured")
	}
	if p.DefaultNodeMonthly != nil {
		parts = append(parts, fmt.Sprintf("default node %s%s/mo", sym, formatMoney(*p.DefaultNodeMonthly)))
	}
	if p.AssumedNodeMonthly != nil {
		parts = append(parts, fmt.Sprintf("assumed node %s%s/mo", sym, formatMoney(*p.AssumedNodeMonthly)))
	}
	if n := len(p.NodeRules); n > 0 {
		parts = append(parts, fmt.Sprintf("%d node rule%s", n, idlePlural(n)))
	}
	if n := len(p.ServiceRules); n > 0 {
		parts = append(parts, fmt.Sprintf("%d service rule%s", n, idlePlural(n)))
	}
	return strings.Join(parts, ", ") + "."
}

func idlePlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// csvSafe neutralises spreadsheet formula injection: service names come from
// observed process and workload names, which whoever runs a process controls.
// Spreadsheets ignore leading whitespace and control characters when deciding
// whether a cell is a formula, so those are looked past first.
func csvSafe(s string) string {
	trimmed := strings.TrimLeftFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f })
	if trimmed == "" {
		return s
	}
	leading := s[:len(s)-len(trimmed)]
	if strings.ContainsRune("=+-@", rune(trimmed[0])) || strings.ContainsAny(leading, "\t\r") {
		return "'" + s
	}
	return s
}
