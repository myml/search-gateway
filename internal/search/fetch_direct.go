package search

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	searchconfig "search-gateway/internal/config"
)

var (
	reTitle     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reComments  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBlockTags = regexp.MustCompile(`(?is)</?(?:p|div|br|li|tr|h[1-6]|section|article|header|footer|blockquote|pre|table|ul|ol|nav|figure|figcaption|main|aside|form|hr|dd|dt|dl)[^>]*>`)
	reAnyTag    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces    = regexp.MustCompile(`[ \t\f\v]+`)
	reBlankRuns = regexp.MustCompile(`\n{3,}`)
	dropTags    = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`),
		regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`),
		regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`),
		regexp.MustCompile(`(?is)<svg[^>]*>.*?</svg>`),
		regexp.MustCompile(`(?is)<iframe[^>]*>.*?</iframe>`),
		regexp.MustCompile(`(?is)<template[^>]*>.*?</template>`),
		regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`),
	}
)

// extractHTML returns a page title and a plain-text rendering of the body.
func extractHTML(page string) (string, string) {
	var title string
	if m := reTitle.FindStringSubmatch(page); len(m) == 2 {
		title = strings.TrimSpace(html.UnescapeString(reAnyTag.ReplaceAllString(m[1], "")))
	}
	body := reComments.ReplaceAllString(page, " ")
	for _, re := range dropTags {
		body = re.ReplaceAllString(body, " ")
	}
	body = reBlockTags.ReplaceAllString(body, "\n")
	body = reAnyTag.ReplaceAllString(body, " ")
	body = html.UnescapeString(body)
	body = strings.ReplaceAll(body, "\u00a0", " ")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = reSpaces.ReplaceAllString(body, " ")

	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	body = strings.Join(lines, "\n")
	body = reBlankRuns.ReplaceAllString(body, "\n\n")
	return title, strings.TrimSpace(body)
}

type directFetch struct {
	name, userAgent string
	client          *http.Client
	maxBytes        int64
}

func newDirectFetch(cfg searchconfig.Provider, client *http.Client, userAgent string) (Fetcher, error) {
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}
	return &directFetch{name: cfg.Name, userAgent: userAgent, client: client, maxBytes: maxBytes}, nil
}

func (p *directFetch) Name() string { return p.name }

func (p *directFetch) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid url %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build fetch request: %w", err)
	}
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.8,*/*;q=0.5")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("fetch %s: HTTP %d: %s", rawURL, resp.StatusCode, truncate(string(data), 200))
	}
	contentType := resp.Header.Get("Content-Type")
	body := string(data)
	title := ""
	content := ""
	if strings.Contains(strings.ToLower(contentType), "html") {
		title, content = extractHTML(body)
	} else {
		content = strings.TrimSpace(body)
	}
	return &FetchResult{URL: rawURL, Title: title, Content: content, ContentType: contentType, StatusCode: resp.StatusCode}, nil
}
