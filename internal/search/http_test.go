package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamErrorSanitized(t *testing.T) {
	cloudflare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head><body>blocked</body></html>`)
	}))
	defer cloudflare.Close()

	_, status, err := doRequest(context.Background(), http.DefaultClient, http.MethodPost, cloudflare.URL, nil, nil, "test")
	if status != http.StatusForbidden || err == nil {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !strings.Contains(err.Error(), "Cloudflare") || strings.Contains(err.Error(), "<html") {
		t.Fatalf("unhelpful error: %v", err)
	}

	jsonErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": "invalid api key"}`)
	}))
	defer jsonErr.Close()
	_, _, err = doRequest(context.Background(), http.DefaultClient, http.MethodPost, jsonErr.URL, nil, nil, "test")
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("json error not surfaced: %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("error should be single line: %q", err)
	}
}
