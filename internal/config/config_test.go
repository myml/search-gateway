package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestQuotaPeriodUsesConfiguredTimezone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"quota_timezone": "Asia/Shanghai"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-31T16:30:00Z is already 2026-11-01 00:30 in Shanghai.
	at := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	if got := cfg.QuotaPeriod(at); got != "2026-11" {
		t.Fatalf("period = %q", got)
	}
	start, end := cfg.QuotaPeriodBounds(at)
	if want := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Fatalf("start = %s, want %s", start, want)
	}
	if want := time.Date(2026, 11, 30, 16, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("end = %s, want %s", end, want)
	}
}

func TestQuotaTimezoneDefaultsToUTC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	if got := cfg.QuotaPeriod(at); got != "2026-10" {
		t.Fatalf("period = %q", got)
	}
	if cfg.QuotaLocation() != time.UTC {
		t.Fatalf("location = %s", cfg.QuotaLocation())
	}
}

func TestLoadRejectsInvalidQuotaTimezone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"quota_timezone": "Mars/Olympus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an invalid quota_timezone")
	}
}

func TestQuotaTimezoneExpandsEnv(t *testing.T) {
	t.Setenv("TEST_QUOTA_TZ", "Asia/Shanghai")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"quota_timezone": "${TEST_QUOTA_TZ}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QuotaTimezone != "Asia/Shanghai" {
		t.Fatalf("quota_timezone = %q", cfg.QuotaTimezone)
	}
	if got := cfg.QuotaPeriod(time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)); got != "2026-11" {
		t.Fatalf("period = %q", got)
	}
}
