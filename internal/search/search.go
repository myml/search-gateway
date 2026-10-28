// Package search implements normalized providers for search and fetch.
package search

import (
	"context"
	"errors"
)

// ErrProviderNotFound means the caller named a provider that is not registered.
var ErrProviderNotFound = errors.New("provider not found")

// Result is one normalized search source.
type Result struct {
	URL     string
	Title   string
	Snippet string
	Date    string // RFC3339 or free-form "page age"; empty means unknown
}

// FetchResult is one normalized fetched page.
type FetchResult struct {
	URL         string
	Title       string
	Content     string
	ContentType string
	StatusCode  int
}

// SearchRequest is one normalized search request.
type SearchRequest struct {
	Query            string
	Count            int
	OriginalQuestion string
}

// Provider searches the web.
type Provider interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]Result, error)
}

// Fetcher retrieves one URL.
type Fetcher interface {
	Name() string
	Fetch(ctx context.Context, rawURL string) (*FetchResult, error)
}
