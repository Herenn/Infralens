package config

import (
	"strings"
	"testing"
	"time"

	"github.com/Herenn/Infralens/backend/service"
)

func TestParsePriceRules(t *testing.T) {
	tests := []struct {
		raw          string
		want         []service.PriceRule
		wantWarnings int
	}{
		{"", nil, 0},
		{"gpu-*=1800", []service.PriceRule{{Pattern: "gpu-*", Monthly: 1800}}, 0},
		{"a=1; b=$2.50\nc = 3", []service.PriceRule{{Pattern: "a", Monthly: 1}, {Pattern: "b", Monthly: 2.5}, {Pattern: "c", Monthly: 3}}, 0},
		{"novalue=,=5,justtext,neg=-1,nan=NaN,ok=7", []service.PriceRule{{Pattern: "ok", Monthly: 7}}, 5},
		{"10.0.1.10/nginx=40", []service.PriceRule{{Pattern: "10.0.1.10/nginx", Monthly: 40}}, 0},
	}
	for _, tt := range tests {
		got, warnings := ParsePriceRules("X", tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("ParsePriceRules(%q) = %+v, want %+v", tt.raw, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParsePriceRules(%q)[%d] = %+v, want %+v", tt.raw, i, got[i], tt.want[i])
			}
		}
		if len(warnings) != tt.wantWarnings {
			t.Errorf("ParsePriceRules(%q) warnings = %v, want %d", tt.raw, warnings, tt.wantWarnings)
		}
		for _, w := range warnings {
			if !strings.HasPrefix(w, "X entry") {
				t.Errorf("warning should name the variable: %q", w)
			}
		}
	}
}

func TestLoadIdleCost(t *testing.T) {
	t.Run("unset is unpriced with default warmup sentinel", func(t *testing.T) {
		for _, k := range []string{"IDLE_COST_NODE_MONTHLY", "IDLE_COST_NODE_PRICES", "IDLE_COST_SERVICE_PRICES", "IDLE_COST_WARMUP", "IDLE_COST_CURRENCY"} {
			t.Setenv(k, "")
		}
		c := Load().IdleCost
		if c.Priced() {
			t.Errorf("expected unpriced, got %+v", c)
		}
		if c.Warmup >= 0 {
			t.Errorf("unset warmup should be negative (default), got %v", c.Warmup)
		}
		if c.Currency != "USD" {
			t.Errorf("currency = %q", c.Currency)
		}
	})
	t.Run("configured", func(t *testing.T) {
		t.Setenv("IDLE_COST_NODE_MONTHLY", "$1,240.50")
		t.Setenv("IDLE_COST_NODE_PRICES", "gpu-*=1800;broken")
		t.Setenv("IDLE_COST_SERVICE_PRICES", "billing-db=310")
		t.Setenv("IDLE_COST_EXCLUDE", "nightly-*, dr-standby")
		t.Setenv("IDLE_COST_WARMUP", "0s")
		t.Setenv("IDLE_COST_QUIET_AFTER", "48h")
		t.Setenv("IDLE_COST_CURRENCY", "eur")
		c := Load().IdleCost
		if c.DefaultNodeMonthly != 1240.5 {
			t.Errorf("default = %v", c.DefaultNodeMonthly)
		}
		if len(c.NodePrices) != 1 || len(c.ServicePrices) != 1 || len(c.Exclude) != 2 {
			t.Errorf("rules = %+v / %+v / %+v", c.NodePrices, c.ServicePrices, c.Exclude)
		}
		if len(c.Warnings) != 1 {
			t.Errorf("warnings = %v", c.Warnings)
		}
		if c.Warmup != 0 || c.QuietAfter != 48*time.Hour || c.Currency != "EUR" {
			t.Errorf("warmup/quiet/currency = %v/%v/%v", c.Warmup, c.QuietAfter, c.Currency)
		}
	})
	t.Run("bad default is a warning, not a crash", func(t *testing.T) {
		t.Setenv("IDLE_COST_NODE_MONTHLY", "lots")
		c := Load().IdleCost
		if c.DefaultNodeMonthly != 0 || len(c.Warnings) == 0 {
			t.Errorf("got %+v", c)
		}
	})
}

// Reviewer repro: IDLE_COST_QUIET_AFTER=7d silently became 72h.
func TestIdleCostDurationsWarnAndAcceptDays(t *testing.T) {
	t.Setenv("IDLE_COST_QUIET_AFTER", "7d")
	t.Setenv("IDLE_COST_WINDOW", "1d12h")
	t.Setenv("IDLE_COST_WARMUP", "soon")
	c := Load().IdleCost
	if c.QuietAfter != 7*24*time.Hour {
		t.Errorf("7d parsed as %v", c.QuietAfter)
	}
	if c.Window != 36*time.Hour {
		t.Errorf("1d12h parsed as %v", c.Window)
	}
	joined := strings.Join(c.Warnings, "\n")
	if !strings.Contains(joined, "IDLE_COST_WARMUP") {
		t.Errorf("bad warmup not warned: %v", c.Warnings)
	}
	if !strings.Contains(joined, "shorter than IDLE_COST_QUIET_AFTER") {
		t.Errorf("window < quiet-after not warned: %v", c.Warnings)
	}
	if c.Warmup >= 0 {
		t.Errorf("bad warmup should fall back to the default sentinel, got %v", c.Warmup)
	}

	t.Setenv("IDLE_COST_QUIET_AFTER", "-5h")
	if c := Load().IdleCost; c.QuietAfter != 0 || len(c.Warnings) == 0 {
		t.Errorf("negative quiet-after accepted: %+v", c)
	}
	t.Setenv("IDLE_COST_CURRENCY", "dollars")
	if c := Load().IdleCost; c.Currency != "USD" || !strings.Contains(strings.Join(c.Warnings, " "), "IDLE_COST_CURRENCY") {
		t.Errorf("invalid currency: %+v", c)
	}
}
