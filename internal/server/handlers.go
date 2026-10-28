package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"search-gateway/internal/logstore"
	"search-gateway/internal/routing"
	"search-gateway/internal/search"
)

type resultJSON struct {
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Date    *string `json:"date"` // explicit null is required by the DeepSeek contract
}

func toResultJSON(results []search.Result) []resultJSON {
	out := make([]resultJSON, 0, len(results))
	for _, r := range results {
		var date *string
		if r.Date != "" {
			d := r.Date
			date = &d
		}
		out = append(out, resultJSON{URL: r.URL, Title: r.Title, Snippet: r.Snippet, Date: date})
	}
	return out
}

// GET / — the embedded console; every other unmatched GET is a JSON 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.serveIndex(w)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"version":  Version,
		"channels": len(snapshot.Channels),
		"routes":   len(snapshot.Routes),
		"api_keys": len(snapshot.APIKeys),
		"revision": snapshot.Revision,
	})
}

// GET /api/info — gateway-side discovery of the caller's route.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	call := callFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "search-gateway",
		"version": Version,
		"route": map[string]any{
			"id":   call.Route.ID,
			"name": call.Route.Name,
			"mode": call.Route.Mode,
		},
		"endpoints": []string{
			"GET /search?q=<query>&count=<n>",
			"GET /fetch?url=<url>",
			"POST /v1/tools/search",
			"POST /v1/tools/fetch",
			"POST /v1/messages",
			"POST /mcp",
		},
	})
}

func setRouteHeaders(w http.ResponseWriter, meta routing.Meta) {
	if meta.RouteName != "" {
		w.Header().Set("X-Search-Route", meta.RouteName)
	}
	if len(meta.Channels) > 0 {
		w.Header().Set("X-Search-Channels", strings.Join(meta.Channels, ","))
		w.Header().Set("X-Search-Provider", meta.Channels[0])
	}
}

func (s *Server) runSearch(r *http.Request, req search.SearchRequest, protocol, query string) ([]search.Result, routing.Meta, error) {
	call := callFrom(r)
	start := time.Now()
	results, meta, err := s.engine.Search(r.Context(), call.Route, req)
	s.recordCall(r, call, logstore.Entry{
		Protocol:    protocol,
		Query:       query,
		ResultCount: len(results),
		Channels:    meta.Channels,
		Mode:        meta.Mode,
	}, start, err)
	return results, meta, err
}

func (s *Server) runFetch(r *http.Request, rawURL, protocol string) (*search.FetchResult, routing.Meta, error) {
	call := callFrom(r)
	start := time.Now()
	result, meta, err := s.engine.Fetch(r.Context(), call.Route, rawURL)
	s.recordCall(r, call, logstore.Entry{
		Protocol: protocol,
		URL:      rawURL,
		Channels: meta.Channels,
		Mode:     meta.Mode,
	}, start, err)
	return result, meta, err
}

func (s *Server) countFromQuery(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("count")
	if raw == "" {
		return s.cfg.DefaultCount, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("count must be a positive integer")
	}
	return min(n, 50), nil
}

// GET /search?q=...&count=...
func (s *Server) handleGetSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(firstQueryParam(r, "q", "query"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter q is required")
		return
	}
	count, err := s.countFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	results, meta, err := s.runSearch(r, search.SearchRequest{Query: query, Count: count}, "search", query)
	if err != nil {
		s.writeGatewayError(w, err)
		return
	}
	setRouteHeaders(w, meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":    query,
		"route":    meta.RouteName,
		"channels": meta.Channels,
		"results":  toResultJSON(results),
	})
}

// GET /fetch?url=...
func (s *Server) handleGetFetch(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "query parameter url is required")
		return
	}
	result, meta, err := s.runFetch(r, rawURL, "fetch")
	if err != nil {
		s.writeGatewayError(w, err)
		return
	}
	setRouteHeaders(w, meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":          result.URL,
		"title":        result.Title,
		"content":      result.Content,
		"content_type": result.ContentType,
		"status_code":  result.StatusCode,
		"route":        meta.RouteName,
		"channels":     meta.Channels,
	})
}

// POST /v1/tools/search — DeepSeek Search API compatible.
func (s *Server) handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query            string `json:"query"`
		Count            int    `json:"count"`
		NeedContent      bool   `json:"need_content"`
		OriginalQuestion string `json:"ori_question"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Query) == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	count := in.Count
	if count <= 0 {
		count = s.cfg.DefaultCount
	}
	results, meta, err := s.runSearch(r, search.SearchRequest{
		Query:            in.Query,
		Count:            count,
		OriginalQuestion: in.OriginalQuestion,
	}, "search", in.Query)
	if err != nil {
		s.writeGatewayError(w, err)
		return
	}
	setRouteHeaders(w, meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"query":    in.Query,
		"route":    meta.RouteName,
		"channels": meta.Channels,
		"results":  toResultJSON(results),
	})
}

// POST /v1/tools/fetch — DeepSeek Fetch API compatible.
func (s *Server) handleFetchAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.URL) == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	result, meta, err := s.runFetch(r, in.URL, "fetch")
	if err != nil {
		s.writeGatewayError(w, err)
		return
	}
	setRouteHeaders(w, meta)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":      result.URL,
		"title":    result.Title,
		"content":  result.Content,
		"route":    meta.RouteName,
		"channels": meta.Channels,
	})
}

func firstQueryParam(r *http.Request, names ...string) string {
	for _, name := range names {
		if v := r.URL.Query().Get(name); v != "" {
			return v
		}
	}
	return ""
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	return json.NewDecoder(r.Body).Decode(dst)
}
