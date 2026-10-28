package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	searchconfig "search-gateway/internal/config"
)

type bocha struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newBocha(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("bocha provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &bocha{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://api.bochaai.com/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *bocha) Name() string { return p.name }

func (p *bocha) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	payload, err := json.Marshal(map[string]any{
		"query":   req.Query,
		"count":   clampCount(req.Count),
		"summary": true,
	})
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"Authorization": "Bearer " + p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "v1/web-search"), headers, payload, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			WebPages struct {
				Value []struct {
					Name          string `json:"name"`
					URL           string `json:"url"`
					Snippet       string `json:"snippet"`
					Summary       string `json:"summary"`
					DatePublished string `json:"datePublished"`
				} `json:"value"`
			} `json:"webPages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode bocha response: %w", err)
	}
	if out.Code != 0 && out.Code != 200 {
		return nil, fmt.Errorf("bocha error %d: %s", out.Code, out.Msg)
	}
	results := make([]Result, 0, len(out.Data.WebPages.Value))
	for _, r := range out.Data.WebPages.Value {
		results = append(results, Result{
			URL:     r.URL,
			Title:   r.Name,
			Snippet: firstNonEmpty(r.Summary, r.Snippet),
			Date:    r.DatePublished,
		})
	}
	return results, nil
}
