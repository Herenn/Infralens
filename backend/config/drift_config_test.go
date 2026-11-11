package config

import (
	"testing"
	"time"
)

// Drift detection is on by default - it no longer waits for a webhook - and
// DRIFT_ENABLED=false is the way to switch it off.
func TestDriftDetectionEnabledByDefault(t *testing.T) {
	t.Setenv("DRIFT_ENABLED", "")
	t.Setenv("DRIFT_WEBHOOK_URL", "")
	cfg := Load().Drift
	if !cfg.DetectionEnabled {
		t.Error("drift detection should be on by default")
	}
	if cfg.Enabled() {
		t.Error("no webhook configured, but Enabled() reports one")
	}

	t.Setenv("DRIFT_ENABLED", "false")
	if Load().Drift.DetectionEnabled {
		t.Error("DRIFT_ENABLED=false should switch detection off")
	}
}

// Demo mode must not write its synthetic past into the real database.
func TestDemoModeUsesItsOwnDatabase(t *testing.T) {
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_DSN", "")
	t.Setenv("DEMO_MODE", "")
	if got := Load().Storage.DSN; got != "infralens.db" {
		t.Errorf("real mode DSN = %q, want infralens.db", got)
	}
	t.Setenv("DEMO_MODE", "true")
	if got := Load().Storage.DSN; got != DefaultDemoDSN {
		t.Errorf("demo mode DSN = %q, want %q", got, DefaultDemoDSN)
	}
	t.Setenv("DB_DSN", "/tmp/infralens-demo.db") // demo.yml sets it explicitly
	if got := Load().Storage.DSN; got != "/tmp/infralens-demo.db" {
		t.Errorf("explicit DB_DSN in demo mode = %q, want it honoured", got)
	}
}

// HISTORY_MAX_GAP below a few heartbeat periods would fragment node coverage
// into one interval per heartbeat: it is raised to MinHistoryMaxGap.
func TestHistoryMaxGapIsClampedToHeartbeatCadence(t *testing.T) {
	t.Setenv("HISTORY_MAX_GAP", "3s")
	if got := Load().Storage.HistoryMaxGap; got != MinHistoryMaxGap {
		t.Errorf("HISTORY_MAX_GAP=3s -> %s, want %s", got, MinHistoryMaxGap)
	}
	t.Setenv("HISTORY_MAX_GAP", "30s")
	if got := Load().Storage.HistoryMaxGap; got != 30*time.Second {
		t.Errorf("HISTORY_MAX_GAP=30s -> %s", got)
	}
}
