package config

import (
	"reflect"
	"testing"
)

func TestMCPConfig(t *testing.T) {
	t.Setenv("MCP_ENABLED", "")
	t.Setenv("MCP_API_KEY", "")
	t.Setenv("MCP_ALLOWED_ORIGINS", "")
	if cfg := Load().MCP; !cfg.Enabled || cfg.APIKey != "" || len(cfg.AllowedOrigins) != 0 {
		t.Errorf("defaults = %+v, want enabled with no key and no extra origins", cfg)
	}

	t.Setenv("MCP_API_KEY", "reader")
	if got := Load().MCP.APIKey; got != "reader" {
		t.Errorf("MCP_API_KEY = %q", got)
	}

	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("MCP_ALLOWED_ORIGINS", " https://a.example , ,https://b.example:8443 ")
	cfg := Load().MCP
	if cfg.Enabled {
		t.Error("MCP_ENABLED=false should disable the endpoint")
	}
	if want := []string{"https://a.example", "https://b.example:8443"}; !reflect.DeepEqual(cfg.AllowedOrigins, want) {
		t.Errorf("AllowedOrigins = %q, want %q", cfg.AllowedOrigins, want)
	}
}
