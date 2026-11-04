package config

import (
	"strings"
	"testing"
)

func TestDriftConfigValidate(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"", false}, // disabled
		{"https://hooks.slack.com/services/T000/B000/secret", false},
		{"http://receiver.internal:9000/drift", false},
		{"hooks.slack.com/services/T000/B000/secret", true}, // no scheme
		{"ftp://example.com/x", true},
		{"https://", true},
	}
	for _, c := range cases {
		err := DriftConfig{WebhookURL: c.url}.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%q) error = %v, wantErr %v", c.url, err, c.wantErr)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("Validate(%q) leaks the URL: %v", c.url, err)
		}
	}
}

func TestDriftWarmupDistinguishesUnsetFromZero(t *testing.T) {
	t.Setenv("DRIFT_WARMUP", "")
	if got := Load().Drift.Warmup; got >= 0 {
		t.Errorf("unset DRIFT_WARMUP should be negative (use default), got %v", got)
	}
	t.Setenv("DRIFT_WARMUP", "0s")
	if got := Load().Drift.Warmup; got != 0 {
		t.Errorf("DRIFT_WARMUP=0s should disable warmup, got %v", got)
	}
}
