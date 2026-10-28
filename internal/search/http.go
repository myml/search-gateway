package search

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

// newHTTPClient builds a client with optional proxy and TLS override.
func newHTTPClient(proxyURL string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", proxyURL, err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// doRequest performs one HTTP call and returns the body, rejecting non-2xx.
func doRequest(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body []byte, userAgent string) ([]byte, int, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s %s: %w", method, rawURL, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("read %s %s: %w", method, rawURL, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, rawURL, resp.StatusCode, sanitizeUpstreamBody(resp.Header.Get("Content-Type"), data))
	}
	return data, resp.StatusCode, nil
}

func mergeHeaders(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

var htmlTitleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// sanitizeUpstreamBody turns an upstream error body into a short, readable
// message instead of dumping a full HTML/Cloudflare page into the UI and logs.
func sanitizeUpstreamBody(contentType string, body []byte) string {
	text := strings.TrimSpace(string(body))
	lower := strings.ToLower(text)
	if strings.Contains(strings.ToLower(contentType), "html") ||
		strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
		if strings.Contains(lower, "cloudflare") {
			return "Cloudflare challenge: upstream rejected this network; set a proxy for this channel or use another provider"
		}
		if match := htmlTitleRE.FindStringSubmatch(text); len(match) == 2 {
			return "HTML error page: " + truncate(strings.Join(strings.Fields(match[1]), " "), 120)
		}
		return "HTML error page"
	}
	return truncate(strings.Join(strings.Fields(text), " "), 300)
}
