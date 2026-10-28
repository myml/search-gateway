package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	searchconfig "search-gateway/internal/config"
)

type exa struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newExa(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("exa provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &exa{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://api.exa.ai/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *exa) Name() string { return p.name }

func (p *exa) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{
		"query":      req.Query,
		"numResults": clampCount(req.Count),
		"contents": map[string]any{
			"text":       map[string]any{"maxCharacters": 800},
			"highlights": map[string]any{"numSentences": 2, "highlightsPerUrl": 1},
		},
	})
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"x-api-key": p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "search"), headers, payload, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			PublishedDate string   `json:"publishedDate"`
			Text          string   `json:"text"`
			Highlights    []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode exa response: %w", err)
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		snippet := strings.Join(r.Highlights, " ")
		if snippet == "" {
			snippet = truncate(r.Text, 500)
		}
		results = append(results, Result{URL: r.URL, Title: r.Title, Snippet: snippet, Date: r.PublishedDate})
	}
	return results, nil
}
