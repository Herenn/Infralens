package mcp

import (
	"testing"
	"time"
)

func TestParseTimeArg(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"2026-11-04T15:00:00Z", time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)},
		{"2026-11-04T15:00:00+03:00", time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)},
		{"2026-11-04", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)},
		{"2026-11-04T15:00", time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)},
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", now.Add(-7 * 24 * time.Hour)},
		{"2w", now.Add(-14 * 24 * time.Hour)},
		{"1d12h", now.Add(-36 * time.Hour)},
		{"90m", now.Add(-90 * time.Minute)},
		{"1.5h", now.Add(-90 * time.Minute)},
		{"7d ago", now.Add(-7 * 24 * time.Hour)},
		{"-24h", now.Add(-24 * time.Hour)},
		{" 3D AGO ", now.Add(-3 * 24 * time.Hour)},
	}
	for _, tc := range cases {
		got, err := parseTimeArg("at", tc.in, now)
		if err != nil {
			t.Errorf("parseTimeArg(%q) error: %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseTimeArg(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "yesterday", "7x", "d7", "2027-01-01T00:00:00Z", "last tuesday"} {
		if _, err := parseTimeArg("at", bad, now); err == nil {
			t.Errorf("parseTimeArg(%q) succeeded, want an error", bad)
		} else if _, ok := err.(*ToolError); !ok {
			t.Errorf("parseTimeArg(%q) error is %T, want *ToolError so the model sees it", bad, err)
		}
	}
}

func TestParseOptionalTimeEmptyMeansCurrent(t *testing.T) {
	for _, in := range []string{"", "  ", "now", "NOW"} {
		got, err := parseOptionalTime("at", in, time.Now())
		if err != nil || !got.IsZero() {
			t.Errorf("parseOptionalTime(%q) = %v, %v; want zero time (current state)", in, got, err)
		}
	}
}

func TestParseFlexibleDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7d": 7 * 24 * time.Hour, "48h": 48 * time.Hour, "2w": 14 * 24 * time.Hour, "1h30m": 90 * time.Minute, "45s": 45 * time.Second,
	} {
		if got, err := parseFlexibleDuration(in); err != nil || got != want {
			t.Errorf("parseFlexibleDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "h", "7", "7y", "-"} {
		if _, err := parseFlexibleDuration(bad); err == nil {
			t.Errorf("parseFlexibleDuration(%q) succeeded, want an error", bad)
		}
	}
}
