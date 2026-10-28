package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExpandsEnvAndAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_TAVILY_KEY", "tvly-secret")
	t.Setenv("TEST_HEADER_VALUE", "custom")

	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{
		"proxy": "${TEST_PROXY_URL}",
		"auth": {"tokens": ["tok-1"]},
		"search": [
			{"type": "tavily", "api_key_env": "TEST_TAVILY_KEY", "headers": {"X-Custom": "${TEST_HEADER_VALUE}"}}
		],
		"fetch": [{"type": "direct"}]
	}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8787" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if cfg.UserAgent == "" || cfg.DefaultCount != 8 {
		t.Fatalf("defaults: user agent %q, count %d", cfg.UserAgent, cfg.DefaultCount)
	}
	if cfg.TimeoutSeconds != 20 || cfg.FetchTimeoutSeconds != 30 {
		t.Fatalf("timeouts = %d/%d", cfg.TimeoutSeconds, cfg.FetchTimeoutSeconds)
	}
	if len(cfg.Search) != 1 || cfg.Search[0].Name != "tavily" {
		t.Fatalf("search = %+v", cfg.Search)
	}
	if cfg.Search[0].APIKey != "tvly-secret" {
		t.Fatalf("api key = %q", cfg.Search[0].APIKey)
	}
	if got := cfg.Search[0].Headers["X-Custom"]; got != "custom" {
		t.Fatalf("header = %q", got)
	}
	if len(cfg.Fetch) != 1 || cfg.Fetch[0].Name != "direct" {
		t.Fatalf("fetch = %+v", cfg.Fetch)
	}
}

func TestLoadRejectsMissingType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"search": [{"name": "x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a provider without a type")
	}
}

func TestLoadRejectsUnsupportedProviderType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	raw := `{"search": [{"name": "custom", "type": "generic", "base_url": "https://api.example"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for the removed generic type")
	}
}
