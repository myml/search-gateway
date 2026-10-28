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

type brave struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newBrave(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("brave provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &brave{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://api.search.brave.com/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *brave) Name() string { return p.name }

func (p *brave) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	params := url.Values{}
	params.Set("q", req.Query)
	params.Set("count", strconv.Itoa(clampCount(req.Count)))
	rawURL := joinURL(p.baseURL, "res/v1/web/search") + "?" + params.Encode()

	headers := mergeHeaders(map[string]string{"X-Subscription-Token": p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodGet, rawURL, headers, nil, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode brave response: %w", err)
	}
	results := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		results = append(results, Result{
			URL:     r.URL,
			Title:   r.Title,
			Snippet: r.Description,
			Date:    firstNonEmpty(r.PageAge, r.Age),
		})
	}
	return results, nil
}
