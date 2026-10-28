package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	searchconfig "search-gateway/internal/config"
)

// jinaFetch uses the Jina Reader service to turn a page into Markdown.
type jinaFetch struct {
	name, baseURL, apiKey, userAgent string
	headers                          map[string]string
	client                           *http.Client
}

func newJinaFetch(cfg searchconfig.Provider, client *http.Client, userAgent string) (Fetcher, error) {
	return &jinaFetch{
		name:      cfg.Name,
		baseURL:   defaultString(cfg.BaseURL, "https://r.jina.ai/"),
		apiKey:    cfg.APIKey,
		userAgent: userAgent,
		headers:   cfg.Headers,
		client:    client,
	}, nil
}

func (p *jinaFetch) Name() string { return p.name }

func (p *jinaFetch) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid url %q", rawURL)
	}
	endpoint := strings.TrimRight(p.baseURL, "/") + "/" + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build jina request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jina fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read jina response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jina fetch %s: HTTP %d: %s", rawURL, resp.StatusCode, truncate(string(data), 200))
	}
	content := strings.TrimSpace(string(data))
	title := ""
	if line, _, ok := strings.Cut(content, "\n"); ok && strings.HasPrefix(line, "# ") {
		title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
	}
	return &FetchResult{URL: rawURL, Title: title, Content: content, ContentType: "text/markdown", StatusCode: resp.StatusCode}, nil
}
