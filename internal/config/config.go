// Package config loads and validates the gateway configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config is the top-level gateway configuration.
type Config struct {
	Listen              string     `json:"listen"`
	StorePath           string     `json:"store_path"`
	DBPath              string     `json:"db_path"`
	LogPath             string     `json:"log_path"` // legacy JSONL import source
	UserAgent           string     `json:"user_agent"`
	Proxy               string     `json:"proxy"`
	InsecureSkipVerify  bool       `json:"insecure_skip_verify"`
	FallbackOnEmpty     bool       `json:"fallback_on_empty"`
	TimeoutSeconds      int        `json:"timeout_seconds"`
	FetchTimeoutSeconds int        `json:"fetch_timeout_seconds"`
	DefaultCount        int        `json:"default_count"`
	AdminTokens         []string   `json:"admin_tokens"`
	Auth                Auth       `json:"auth"`
	Search              []Provider `json:"search"`
	Fetch               []Provider `json:"fetch"`
}

// Auth configures the bearer tokens accepted by the HTTP surface.
type Auth struct {
	Tokens []string `json:"tokens"`
}

// Provider configures one search or fetch backend. Fields not used by a type
// are ignored. All string values support ${ENV} expansion.
type Provider struct {
	Name               string            `json:"name"`
	Type               string            `json:"type"`
	APIKey             string            `json:"api_key"`
	APIKeyEnv          string            `json:"api_key_env"`
	BaseURL            string            `json:"base_url"`
	Proxy              string            `json:"proxy"`
	Headers            map[string]string `json:"headers"`
	InsecureSkipVerify *bool             `json:"insecure_skip_verify"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	MaxBytes           int64             `json:"max_bytes"`
	UserAgent          string            `json:"user_agent"`
}

// Load reads, expands and validates a JSON configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.expandEnv()
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) expandEnv() {
	c.Listen = os.ExpandEnv(c.Listen)
	c.StorePath = os.ExpandEnv(c.StorePath)
	c.DBPath = os.ExpandEnv(c.DBPath)
	c.LogPath = os.ExpandEnv(c.LogPath)
	c.UserAgent = os.ExpandEnv(c.UserAgent)
	c.Proxy = os.ExpandEnv(c.Proxy)
	for i := range c.AdminTokens {
		c.AdminTokens[i] = os.ExpandEnv(c.AdminTokens[i])
	}
	for i := range c.Auth.Tokens {
		c.Auth.Tokens[i] = os.ExpandEnv(c.Auth.Tokens[i])
	}
	for _, list := range [][]Provider{c.Search, c.Fetch} {
		for i := range list {
			p := &list[i]
			p.Name = os.ExpandEnv(p.Name)
			p.Type = os.ExpandEnv(p.Type)
			p.APIKey = os.ExpandEnv(p.APIKey)
			p.APIKeyEnv = os.ExpandEnv(p.APIKeyEnv)
			if p.APIKey == "" && p.APIKeyEnv != "" {
				p.APIKey = os.Getenv(p.APIKeyEnv)
			}
			p.BaseURL = os.ExpandEnv(p.BaseURL)
			p.Proxy = os.ExpandEnv(p.Proxy)
			for k, v := range p.Headers {
				p.Headers[k] = os.ExpandEnv(v)
			}
		}
	}
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8787"
	}
	if c.StorePath == "" {
		c.StorePath = "./data/store.json"
	}
	if c.DBPath == "" {
		c.DBPath = "./data/gateway.db"
	}
	if c.LogPath == "" {
		c.LogPath = "./data/requests.jsonl"
	}
	if c.UserAgent == "" {
		c.UserAgent = "search-gateway/0.1"
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 20
	}
	if c.FetchTimeoutSeconds <= 0 {
		c.FetchTimeoutSeconds = 30
	}
	if c.DefaultCount <= 0 {
		c.DefaultCount = 8
	}
	if c.DefaultCount > 50 {
		c.DefaultCount = 50
	}
	for _, list := range [][]Provider{c.Search, c.Fetch} {
		for i := range list {
			if list[i].Name == "" {
				list[i].Name = list[i].Type
			}
		}
	}
}

var searchProviderTypes = map[string]struct{}{
	"tavily": {}, "brave": {}, "exa": {}, "serper": {}, "bocha": {}, "searxng": {},
}

var fetchProviderTypes = map[string]struct{}{
	"direct": {}, "jina": {},
}

func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	for _, p := range c.Search {
		if _, ok := searchProviderTypes[p.Type]; !ok {
			return fmt.Errorf("search provider %q: unsupported type %q", p.Name, p.Type)
		}
	}
	for _, p := range c.Fetch {
		if _, ok := fetchProviderTypes[p.Type]; !ok {
			return fmt.Errorf("fetch provider %q: unsupported type %q", p.Name, p.Type)
		}
	}
	return nil
}

// SearchTimeout is the per-request search budget.
func (c *Config) SearchTimeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// FetchTimeout is the per-request fetch budget.
func (c *Config) FetchTimeout() time.Duration {
	return time.Duration(c.FetchTimeoutSeconds) * time.Second
}

// ProviderTimeout returns the provider-specific timeout override or fallback.
func (c *Config) ProviderTimeout(p Provider, fallback time.Duration) time.Duration {
	if p.TimeoutSeconds > 0 {
		return time.Duration(p.TimeoutSeconds) * time.Second
	}
	return fallback
}
