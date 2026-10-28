package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	searchconfig "search-gateway/internal/config"
)

type serper struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newSerper(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("serper provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &serper{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://google.serper.dev/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *serper) Name() string { return p.name }

func (p *serper) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{
		"q":   req.Query,
		"num": clampCount(req.Count),
	})
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"X-API-KEY": p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "search"), headers, payload, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Organic []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode serper response: %w", err)
	}
	results := make([]Result, 0, len(out.Organic))
	for _, r := range out.Organic {
		results = append(results, Result{URL: r.Link, Title: r.Title, Snippet: r.Snippet, Date: r.Date})
	}
	return results, nil
}
