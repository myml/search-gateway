package search

import (
	"fmt"
	"net/http"
	"time"

	"search-gateway/internal/config"
)

// defaultFetchUserAgent is used for direct page fetching. Many sites (Baidu
// included) return a tiny JS redirect stub to non-browser user agents.
const defaultFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// NewClient builds an outbound HTTP client with optional proxy and TLS override.
func NewClient(proxyURL string, insecure bool, timeout time.Duration) (*http.Client, error) {
	return newHTTPClient(proxyURL, insecure, timeout)
}

// BuildSearchProvider constructs one search provider from configuration.
func BuildSearchProvider(pc config.Provider, client *http.Client, userAgent string) (Provider, error) {
	if pc.UserAgent == "" {
		pc.UserAgent = userAgent
	}
	switch pc.Type {
	case "tavily":
		return newTavily(pc, client, pc.UserAgent)
	case "brave":
		return newBrave(pc, client, pc.UserAgent)
	case "exa":
		return newExa(pc, client, pc.UserAgent)
	case "serper":
		return newSerper(pc, client, pc.UserAgent)
	case "bocha":
		return newBocha(pc, client, pc.UserAgent)
	case "searxng":
		return newSearxng(pc, client, pc.UserAgent)
	default:
		return nil, fmt.Errorf("channel type %q cannot search", pc.Type)
	}
}

// BuildFetchProvider constructs one fetch provider from configuration.
func BuildFetchProvider(pc config.Provider, client *http.Client, userAgent string) (Fetcher, error) {
	if pc.UserAgent == "" {
		if pc.Type == "direct" {
			pc.UserAgent = defaultFetchUserAgent
		} else {
			pc.UserAgent = userAgent
		}
	}
	switch pc.Type {
	case "direct":
		return newDirectFetch(pc, client, pc.UserAgent)
	case "jina":
		return newJinaFetch(pc, client, pc.UserAgent)
	default:
		return nil, fmt.Errorf("channel type %q cannot fetch", pc.Type)
	}
}
