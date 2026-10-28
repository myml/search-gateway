package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	searchconfig "search-gateway/internal/config"
)

type searxng struct {
	name, baseURL, userAgent string
	headers                  map[string]string
	client                   *http.Client
}

func newSearxng(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("searxng provider %q: base_url is required", cfg.Name)
	}
	return &searxng{
		name:      cfg.Name,
		baseURL:   cfg.BaseURL,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *searxng) Name() string { return p.name }

func (p *searxng) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	params := url.Values{}
	params.Set("q", req.Query)
	params.Set("format", "json")
	params.Set("count", strconv.Itoa(clampCount(req.Count)))
	rawURL := joinURL(p.baseURL, "search") + "?" + params.Encode()

	data, _, err := doRequest(ctx, p.client, http.MethodGet, rawURL, p.headers, nil, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode searxng response: %w", err)
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, Result{URL: r.URL, Title: r.Title, Snippet: r.Content, Date: r.PublishedDate})
	}
	return results, nil
}
