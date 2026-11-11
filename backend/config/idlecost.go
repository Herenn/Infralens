package config

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Herenn/Infralens/backend/service"
)

// loadIdleCost reads idle-spend estimation settings.
//
// Nothing here has a built-in price. Cloud list prices go stale within months
// and vary by region, commitment and discount, so a shipped table would be a
// confident wrong number in exactly the place people copy figures from. With
// nothing set, the idle-spend report lists idle services and their evidence
// with null costs.
//
//	IDLE_COST_NODE_MONTHLY    default monthly cost of one node, e.g. "140"
//	IDLE_COST_NODE_PRICES     per-node overrides, glob=price: "gpu-*=1800,db-1=420"
//	IDLE_COST_SERVICE_PRICES  per-service prices (name/ID glob=price), priced
//	                          directly instead of as a node share: "billing-db=310"
//	IDLE_COST_CURRENCY        display label, default "USD" (no conversion)
//	IDLE_COST_EXCLUDE         globs never flagged: "nightly-*,dr-standby"
//	IDLE_COST_WINDOW          history consulted, default 30d (clamped to HISTORY_RETENTION)
//	IDLE_COST_QUIET_AFTER     no inbound for this long to be flagged, default 72h
//	IDLE_COST_WARMUP          ignore services younger than this, default 24h; 0 disables
//
// Malformed entries are skipped with a warning that is logged at startup and
// shown in every report, rather than failing startup: a typo in a price must
// not take the topology view down with it.
func loadIdleCost() service.IdleCostConfig {
	cfg := service.IdleCostConfig{
		Currency: strings.ToUpper(strings.TrimSpace(getEnv("IDLE_COST_CURRENCY", "USD"))),
		Source:   "env",
	}
	if !currencyCode.MatchString(cfg.Currency) {
		cfg.Warnings = append(cfg.Warnings, "IDLE_COST_CURRENCY ignored: expected a three-letter code such as USD or EUR")
		cfg.Currency = "USD"
	}

	// Durations are parsed here rather than with getDurationEnv, which
	// silently keeps the default on a typo - and for these, the default is
	// the dangerous direction (IDLE_COST_QUIET_AFTER=7d quietly becoming 72h
	// flags services after three days instead of seven).
	var warn string
	cfg.Window, warn = idleDurationEnv("IDLE_COST_WINDOW", 0)
	cfg.Warnings = appendNonEmpty(cfg.Warnings, warn)
	cfg.QuietAfter, warn = idleDurationEnv("IDLE_COST_QUIET_AFTER", 0)
	cfg.Warnings = appendNonEmpty(cfg.Warnings, warn)
	// -1 = unset, so IDLE_COST_WARMUP=0s can mean "no warmup".
	cfg.Warmup, warn = idleDurationEnv("IDLE_COST_WARMUP", -1)
	cfg.Warnings = appendNonEmpty(cfg.Warnings, warn)

	window, quiet := cfg.Window, cfg.QuietAfter
	if window <= 0 {
		window = service.DefaultIdleWindow
	}
	if quiet <= 0 {
		quiet = service.DefaultIdleQuietAfter
	}
	if window < quiet {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
			"IDLE_COST_WINDOW (%s) is shorter than IDLE_COST_QUIET_AFTER (%s): nothing can ever be quiet long enough to be flagged", window, quiet))
	}

	if raw := strings.TrimSpace(os.Getenv("IDLE_COST_NODE_MONTHLY")); raw != "" {
		v, err := parseMoney(raw)
		if err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("IDLE_COST_NODE_MONTHLY ignored: %v", err))
		} else {
			cfg.DefaultNodeMonthly = v
		}
	}

	var warnings []string
	cfg.NodePrices, warnings = ParsePriceRules("IDLE_COST_NODE_PRICES", os.Getenv("IDLE_COST_NODE_PRICES"))
	cfg.Warnings = append(cfg.Warnings, warnings...)
	cfg.ServicePrices, warnings = ParsePriceRules("IDLE_COST_SERVICE_PRICES", os.Getenv("IDLE_COST_SERVICE_PRICES"))
	cfg.Warnings = append(cfg.Warnings, warnings...)
	cfg.Exclude = splitList(os.Getenv("IDLE_COST_EXCLUDE"))

	return cfg
}

// ParsePriceRules parses "pattern=price" entries separated by commas,
// semicolons or newlines. Order is preserved: the first matching rule wins.
// Invalid entries are dropped and described in the returned warnings, which
// name the variable but never echo more than the offending entry.
func ParsePriceRules(name, raw string) ([]service.PriceRule, []string) {
	var rules []service.PriceRule
	var warnings []string
	for _, entry := range splitList(raw) {
		i := strings.LastIndex(entry, "=")
		if i <= 0 || i == len(entry)-1 {
			warnings = append(warnings, fmt.Sprintf("%s entry %q ignored: expected pattern=monthly_price", name, entry))
			continue
		}
		pattern := strings.TrimSpace(entry[:i])
		price, err := parseMoney(entry[i+1:])
		if pattern == "" || err != nil {
			warnings = append(warnings, fmt.Sprintf("%s entry %q ignored: %v", name, entry, err))
			continue
		}
		rules = append(rules, service.PriceRule{Pattern: pattern, Monthly: price})
	}
	return rules, warnings
}

// parseMoney accepts "140", "140.50", "$140", "1,240" - a non-negative
// finite amount.
func parseMoney(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimLeft(s, "$€£¥₹₺ ")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "_", "")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("%q is not a non-negative amount", strings.TrimSpace(raw))
	}
	return v, nil
}

// splitList splits on commas, semicolons and newlines, dropping blanks. A
// comma inside an amount ("1,240") would be ambiguous here, so price lists
// should use plain numbers or ";" as the separator.
func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// idleDurationEnv reads a duration that also accepts a day suffix ("7d",
// "1d12h"). Unset returns fallback; anything unparseable or negative also
// returns fallback, but with a warning naming the variable.
func idleDurationEnv(key string, fallback time.Duration) (time.Duration, string) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, ""
	}
	d, err := parseDurationDays(raw)
	if err != nil || d < 0 {
		return fallback, fmt.Sprintf("%s=%q ignored: expected a duration such as 72h or 7d; using the default", key, raw)
	}
	return d, ""
}

// parseDurationDays is time.ParseDuration plus a leading whole-day term.
func parseDurationDays(s string) (time.Duration, error) {
	if i := strings.Index(s, "d"); i > 0 {
		days, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, err
		}
		d := time.Duration(days) * 24 * time.Hour
		if rest := s[i+1:]; rest != "" {
			r, err := time.ParseDuration(rest)
			if err != nil {
				return 0, err
			}
			d += r
		}
		return d, nil
	}
	return time.ParseDuration(s)
}

func appendNonEmpty(xs []string, s string) []string {
	if s == "" {
		return xs
	}
	return append(xs, s)
}
