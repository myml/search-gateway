package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"search-gateway/internal/config"
)

func TestDeepSeekSearch(t *testing.T) {
	var (
		path, auth string
		payload    map[string]any
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		payload = nil
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "One", "url": "https://example.com/a", "snippet": "short", "summary": "long summary", "date": "2026-01-02T03:04:05Z"},
				{"title": "Two", "url": "https://example.com/b", "snippet": "only snippet", "summary": "", "date": nil},
			},
		})
	}))
	defer upstream.Close()

	provider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL + "/"}, http.DefaultClient, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.Search(context.Background(), SearchRequest{Query: "hello", Count: 3, OriginalQuestion: "what does it mean"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/tools/search" || auth != "Bearer secret" {
		t.Fatalf("path = %q, authorization = %q", path, auth)
	}
	if payload["query"] != "hello" || payload["count"] != float64(3) || payload["ori_question"] != "what does it mean" {
		t.Fatalf("payload = %+v", payload)
	}
	if _, ok := payload["need_content"]; ok {
		t.Fatalf("need_content should keep the upstream default: %+v", payload)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].URL != "https://example.com/a" || results[0].Title != "One" ||
		results[0].Snippet != "long summary" || results[0].Date != "2026-01-02T03:04:05Z" {
		t.Fatalf("first result = %+v", results[0])
	}
	// summary missing: fall back to the short snippet, null date becomes empty.
	if results[1].Snippet != "only snippet" || results[1].Date != "" {
		t.Fatalf("second result = %+v", results[1])
	}
}

func TestDeepSeekSearchSkipsLongOriginalQuestion(t *testing.T) {
	var payload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload = nil
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()

	provider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	longQuestion := strings.Repeat("长", deepSeekMaxQueryRunes+1)
	if _, err := provider.Search(context.Background(), SearchRequest{Query: "q", Count: 1, OriginalQuestion: longQuestion}); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["ori_question"]; ok {
		t.Fatalf("over-long ori_question should be dropped: %+v", payload)
	}
	if payload["query"] != "q" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestDeepSeekSearchRequiresAPIKey(t *testing.T) {
	if _, err := newDeepSeekSearch(config.Provider{Name: "ds"}, http.DefaultClient, ""); err == nil {
		t.Fatal("expected an error without api_key")
	}
}

func TestDeepSeekSearchClampsCount(t *testing.T) {
	var payload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload = nil
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer upstream.Close()

	provider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), SearchRequest{Query: "q", Count: 500}); err != nil {
		t.Fatal(err)
	}
	if payload["count"] != float64(50) {
		t.Fatalf("count = %v, want the upstream maximum 50", payload["count"])
	}
}

func TestDeepSeekSearchDecodeError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":"not a list"}`))
	}))
	defer upstream.Close()

	provider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), SearchRequest{Query: "q", Count: 1}); err == nil || !strings.Contains(err.Error(), "decode deepseek search response") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepSeekSearchUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit","param":null,"code":"rate_limit"}}`))
	}))
	defer upstream.Close()

	provider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Search(context.Background(), SearchRequest{Query: "hello", Count: 1})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepSeekFetch(t *testing.T) {
	var (
		path, auth string
		payload    map[string]string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		payload = nil
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_ = json.NewEncoder(w).Encode(map[string]string{"title": "Example article", "content": "# Example article\n\nBody."})
	}))
	defer upstream.Close()

	provider, err := newDeepSeekFetch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL + "/"}, http.DefaultClient, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Fetch(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/tools/fetch" || auth != "Bearer secret" || payload["url"] != "https://example.com/article" {
		t.Fatalf("path = %q, authorization = %q, payload = %+v", path, auth, payload)
	}
	if result.URL != "https://example.com/article" || result.Title != "Example article" ||
		result.Content != "# Example article\n\nBody." || result.ContentType != "text/markdown" || result.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v", result)
	}
}

func TestDeepSeekFetchRejectsInvalidURL(t *testing.T) {
	provider, err := newDeepSeekFetch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: "https://api.deepseek.com/"}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range []string{
		"",
		"ftp://example.com/file",
		"https://user:pass@example.com/page",
		"https://example.com/" + strings.Repeat("a", deepSeekMaxFetchURLLength),
	} {
		if _, err := provider.Fetch(context.Background(), rawURL); err == nil {
			t.Fatalf("url %q: expected a validation error", rawURL)
		}
	}
}

func TestDeepSeekFetchUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"fetch could not retrieve content","type":"invalid_request_error","param":null,"code":"not_found"}}`))
	}))
	defer upstream.Close()

	provider, err := newDeepSeekFetch(config.Provider{Name: "ds", APIKey: "secret", BaseURL: upstream.URL}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Fetch(context.Background(), "https://example.com/missing")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "could not retrieve") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepSeekDefaultsToOfficialBaseURL(t *testing.T) {
	searchProvider, err := newDeepSeekSearch(config.Provider{Name: "ds", APIKey: "secret"}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := searchProvider.(*deepSeekSearch).baseURL; got != "https://api.deepseek.com/" {
		t.Fatalf("search base url = %q", got)
	}
	fetchProvider, err := newDeepSeekFetch(config.Provider{Name: "ds", APIKey: "secret"}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := fetchProvider.(*deepSeekFetch).baseURL; got != "https://api.deepseek.com/" {
		t.Fatalf("fetch base url = %q", got)
	}
}

func TestDeepSeekFetchRequiresAPIKey(t *testing.T) {
	if _, err := newDeepSeekFetch(config.Provider{Name: "ds"}, http.DefaultClient, ""); err == nil {
		t.Fatal("expected an error without api_key")
	}
}
