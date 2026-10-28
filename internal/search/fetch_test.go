package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"search-gateway/internal/config"
)

const samplePage = `<html><head><title>Demo &amp; Page</title>
<script>alert("x")</script><style>.x{}</style></head>
<body><article><h1>Heading</h1><p>First paragraph.</p><p>Second&nbsp;paragraph.</p></article></body></html>`

func TestExtractHTML(t *testing.T) {
	title, content := extractHTML(samplePage)
	if title != "Demo & Page" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(content, "First paragraph.") || !strings.Contains(content, "Second paragraph.") {
		t.Fatalf("content = %q", content)
	}
	for _, bad := range []string{"alert", ".x{}", "<p>"} {
		if strings.Contains(content, bad) {
			t.Fatalf("content leaked %q: %q", bad, content)
		}
	}
}

func TestDirectFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, samplePage)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "plain body")
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, err := newHTTPClient("", false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := newDirectFetch(config.Provider{Name: "direct"}, client, "test")
	if err != nil {
		t.Fatal(err)
	}

	htmlResult, err := fetcher.Fetch(context.Background(), ts.URL+"/html")
	if err != nil {
		t.Fatal(err)
	}
	if htmlResult.Title != "Demo & Page" || !strings.Contains(htmlResult.Content, "First paragraph.") {
		t.Fatalf("html result = %+v", htmlResult)
	}

	plainResult, err := fetcher.Fetch(context.Background(), ts.URL+"/plain")
	if err != nil {
		t.Fatal(err)
	}
	if plainResult.Content != "plain body" {
		t.Fatalf("plain result = %+v", plainResult)
	}

	if _, err := fetcher.Fetch(context.Background(), ts.URL+"/missing"); err == nil {
		t.Fatal("expected an error for HTTP 404")
	}
	if _, err := fetcher.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("expected an error for a non-http URL")
	}
}

func TestJinaFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/http://example.com/page") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer jina-key" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "# Example Title\n\nBody text.")
	}))
	defer ts.Close()

	client, err := newHTTPClient("", false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := newJinaFetch(config.Provider{Name: "jina", BaseURL: ts.URL, APIKey: "jina-key"}, client, "test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), "http://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Example Title" || !strings.Contains(result.Content, "Body text.") {
		t.Fatalf("result = %+v", result)
	}
}

func TestDirectFetchUsesBrowserUserAgent(t *testing.T) {
	var userAgent string
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>UA Test</title></head><body><p>hello</p></body></html>`)
	}))
	defer page.Close()

	fetcher, err := BuildFetchProvider(config.Provider{Type: "direct"}, page.Client(), "search-gateway/test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := fetcher.Fetch(context.Background(), page.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(userAgent, "Mozilla/5.0") {
		t.Fatalf("direct fetch UA = %q", userAgent)
	}
	if result.Title != "UA Test" || !strings.Contains(result.Content, "hello") {
		t.Fatalf("result = %+v", result)
	}

	override, err := BuildFetchProvider(config.Provider{Type: "direct", UserAgent: "custom-agent"}, page.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := override.Fetch(context.Background(), page.URL); err != nil {
		t.Fatal(err)
	}
	if userAgent != "custom-agent" {
		t.Fatalf("override UA = %q", userAgent)
	}
}
