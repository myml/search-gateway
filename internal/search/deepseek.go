package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	searchconfig "search-gateway/internal/config"
)

const (
	defaultDeepSeekBaseURL = "https://api.deepseek.com/"
	// deepSeekMaxQueryRunes is the upstream limit for query and ori_question;
	// longer values are rejected with HTTP 400.
	deepSeekMaxQueryRunes = 128
	// deepSeekMaxFetchURLLength is the upstream limit for the Fetch URL.
	deepSeekMaxFetchURLLength = 8192
)

// deepSeekSearch calls DeepSeek's own Search API. It is the upstream channel
// counterpart of the DeepSeek-compatible /v1/tools/search endpoint this
// gateway serves to clients, not the same thing.
type deepSeekSearch struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newDeepSeekSearch(cfg searchconfig.Provider, client *http.Client, userAgent string) (Provider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("deepseek provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &deepSeekSearch{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, defaultDeepSeekBaseURL),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *deepSeekSearch) Name() string { return p.name }

func (p *deepSeekSearch) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	payload := map[string]any{
		"query": req.Query,
		"count": clampCount(req.Count),
	}
	// ori_question is optional and capped at 128 characters upstream; drop an
	// over-long question instead of failing the whole search because of it.
	if q := strings.TrimSpace(req.OriginalQuestion); q != "" && utf8.RuneCountInString(q) <= deepSeekMaxQueryRunes {
		payload["ori_question"] = q
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"Authorization": "Bearer " + p.apiKey}, p.headers)
	data, _, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "v1/tools/search"), headers, body, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Snippet string  `json:"snippet"`
			Summary string  `json:"summary"`
			Date    *string `json:"date"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode deepseek search response: %w", err)
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		result := Result{
			URL:   r.URL,
			Title: r.Title,
			// summary is the long, model-oriented form; fall back to the short
			// display snippet when it is missing.
			Snippet: firstNonEmpty(strings.TrimSpace(r.Summary), strings.TrimSpace(r.Snippet)),
		}
		if r.Date != nil {
			result.Date = *r.Date
		}
		results = append(results, result)
	}
	return results, nil
}

// deepSeekFetch uses DeepSeek's Fetch API, which returns page content as
// Markdown.
type deepSeekFetch struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newDeepSeekFetch(cfg searchconfig.Provider, client *http.Client, userAgent string) (Fetcher, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("deepseek provider %q: api_key or api_key_env is required", cfg.Name)
	}
	return &deepSeekFetch{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, defaultDeepSeekBaseURL),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *deepSeekFetch) Name() string { return p.name }

func (p *deepSeekFetch) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid url %q", rawURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("invalid url %q: credentials are not allowed", rawURL)
	}
	if len(rawURL) > deepSeekMaxFetchURLLength {
		return nil, fmt.Errorf("invalid url: length %d exceeds upstream limit %d", len(rawURL), deepSeekMaxFetchURLLength)
	}
	body, err := json.Marshal(map[string]string{"url": rawURL})
	if err != nil {
		return nil, err
	}
	headers := mergeHeaders(map[string]string{"Authorization": "Bearer " + p.apiKey}, p.headers)
	data, status, err := doRequest(ctx, p.client, http.MethodPost, joinURL(p.baseURL, "v1/tools/fetch"), headers, body, p.userAgent)
	if err != nil {
		return nil, err
	}
	var out struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode deepseek fetch response: %w", err)
	}
	return &FetchResult{
		URL:         rawURL,
		Title:       out.Title,
		Content:     out.Content,
		ContentType: "text/markdown",
		StatusCode:  status,
	}, nil
}
