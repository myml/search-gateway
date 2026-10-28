package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"search-gateway/internal/config"
)

func TestExaSearch(t *testing.T) {
	var path, key string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("x-api-key")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"title": "Example", "url": "https://example.com/a", "publishedDate": "2026-01-02", "text": "full text", "highlights": []string{"one", "two"}},
			},
		})
	}))
	defer upstream.Close()

	provider, err := newExa(config.Provider{Name: "exa", APIKey: "secret", BaseURL: upstream.URL + "/"}, http.DefaultClient, "test")
	if err != nil {
		t.Fatal(err)
	}
	results, err := provider.Search(context.Background(), SearchRequest{Query: "hello", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/search" || key != "secret" {
		t.Fatalf("path=%q key=%q", path, key)
	}
	if len(results) != 1 || results[0].URL != "https://example.com/a" || results[0].Snippet != "one two" || results[0].Date != "2026-01-02" {
		t.Fatalf("results = %+v", results)
	}
}
