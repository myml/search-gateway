package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	searchconfig "search-gateway/internal/config"
)

type tavily struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newTavily(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("tavily provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &tavily{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://api.tavily.com/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *tavily) Name() string { return p.name }

func (p *tavily) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{
		"query":               req.Query,
		"max_results":         clampCount(req.Count),
		"include_answer":      false,
		"include_raw_content": false,
	})
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"Authorization": "Bearer " + p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "search"), headers, payload, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode tavily response: %w", err)
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, Result{URL: r.URL, Title: r.Title, Snippet: r.Content, Date: r.PublishedDate})
	}
	return results, nil
}
