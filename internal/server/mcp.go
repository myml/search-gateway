package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"search-gateway/internal/logstore"
	"search-gateway/internal/search"
	"search-gateway/internal/store"
)

// The MCP implementation is intentionally small and stateless: every JSON-RPC
// message is a POST to /mcp with the API key in the Authorization header. It
// advertises the legacy (pre-2026-07-28) initialize handshake so clients that
// know newer protocol revisions fall back to the interoperable wire format.
const (
	mcpDefaultProtocol = "2025-11-25"
	mcpMaxBodyBytes    = 1 << 20
)

var mcpProtocolVersions = map[string]struct{}{
	"2024-10-07": {},
	"2024-11-05": {},
	"2025-03-26": {},
	"2025-06-18": {},
	"2025-11-25": {},
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// POST /mcp handles one MCP Streamable HTTP JSON-RPC message.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	var req mcpRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, mcpMaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		writeMCPRPCError(w, http.StatusBadRequest, nil, -32700, "parse error: "+err.Error())
		return
	}
	if req.JSONRPC != "2.0" {
		writeMCPRPCError(w, http.StatusBadRequest, req.ID, -32600, "jsonrpc must be 2.0")
		return
	}
	if req.Method == "" {
		writeMCPRPCError(w, http.StatusBadRequest, req.ID, -32600, "method is required")
		return
	}

	// JSON-RPC notifications (no id) are acknowledged with 202 and never
	// produce a response body. MCP uses initialized/cancelled notifications.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		writeMCPResult(w, req.ID, map[string]any{
			"protocolVersion": negotiateMCPVersion(params.ProtocolVersion),
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "search-gateway",
				"version": Version,
			},
			"instructions": "Use web_search to search the web and web_fetch to fetch a page through search-gateway.",
		})
	case "ping":
		writeMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeMCPResult(w, req.ID, map[string]any{"tools": s.mcpTools(r)})
	case "tools/call":
		s.handleMCPToolCall(w, r, req)
	case "server/discover":
		// Newer MCP clients probe this first. Method-not-found is the
		// documented legacy-fallback signal; they then send initialize.
		writeMCPRPCError(w, http.StatusOK, req.ID, -32601, "method not found")
	default:
		writeMCPRPCError(w, http.StatusOK, req.ID, -32601, "method not found: "+req.Method)
	}
}

func negotiateMCPVersion(requested string) string {
	if _, ok := mcpProtocolVersions[requested]; ok {
		return requested
	}
	return mcpDefaultProtocol
}

func (s *Server) mcpTools(r *http.Request) []map[string]any {
	call := callFrom(r)
	tools := make([]map[string]any, 0, 2)
	if routeUsable(call.SearchRoute) {
		tools = append(tools, map[string]any{
			"name":        "web_search",
			"description": "Search the web through the configured search route. Returns normalized title, URL, snippet and date fields.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query.",
					},
					"count": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     50,
						"description": "Number of results to return. Defaults to the gateway setting.",
					},
				},
				"required": []string{"query"},
			},
		})
	}
	if routeUsable(call.FetchRoute) {
		tools = append(tools, map[string]any{
			"name":        "web_fetch",
			"description": "Fetch a web page through the configured fetch route and return its extracted text.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Absolute http(s) URL to fetch.",
					},
				},
				"required": []string{"url"},
			},
		})
	}
	return tools
}

func (s *Server) handleMCPToolCall(w http.ResponseWriter, r *http.Request, req mcpRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeMCPRPCError(w, http.StatusOK, req.ID, -32602, "invalid tools/call params: "+err.Error())
		return
	}
	if strings.TrimSpace(params.Name) == "" {
		writeMCPRPCError(w, http.StatusOK, req.ID, -32602, "tool name is required")
		return
	}

	var result any
	switch params.Name {
	case "web_search":
		result = s.mcpWebSearch(r, params.Arguments)
	case "web_fetch":
		result = s.mcpWebFetch(r, params.Arguments)
	default:
		result = mcpTextResult(fmt.Sprintf("unknown tool %q", params.Name), true)
	}
	writeMCPResult(w, req.ID, result)
}

func (s *Server) mcpWebSearch(r *http.Request, raw json.RawMessage) any {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpTextResult("invalid arguments: "+err.Error(), true)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return mcpTextResult("query is required", true)
	}
	count := args.Count
	if count <= 0 {
		count = s.cfg.DefaultCount
	}
	if count > 50 {
		count = 50
	}

	call := callFrom(r)
	if !routeUsable(call.SearchRoute) {
		return mcpTextResult("this API key has no enabled search route", true)
	}
	start := time.Now()
	results, meta, err := s.engine.Search(r.Context(), call.SearchRoute, search.SearchRequest{Query: query, Count: count})
	call.Route = call.SearchRoute
	s.recordCall(r, call, logstore.Entry{
		Protocol:    "mcp",
		Query:       query,
		ResultCount: len(results),
		Channels:    meta.Channels,
		Mode:        meta.Mode,
	}, start, err)
	if err != nil {
		return mcpTextResult("search failed: "+err.Error(), true)
	}

	items := make([]map[string]any, 0, len(results))
	for _, item := range results {
		items = append(items, map[string]any{
			"title":   item.Title,
			"url":     item.URL,
			"snippet": item.Snippet,
			"date":    item.Date,
		})
	}
	payload := map[string]any{
		"query":    query,
		"route":    meta.RouteName,
		"mode":     meta.Mode,
		"channels": meta.Channels,
		"results":  items,
	}
	return mcpJSONResult(payload)
}

func (s *Server) mcpWebFetch(r *http.Request, raw json.RawMessage) any {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpTextResult("invalid arguments: "+err.Error(), true)
	}
	rawURL := strings.TrimSpace(args.URL)
	if rawURL == "" {
		return mcpTextResult("url is required", true)
	}

	call := callFrom(r)
	if !routeUsable(call.FetchRoute) {
		return mcpTextResult("this API key has no enabled fetch route", true)
	}
	start := time.Now()
	result, meta, err := s.engine.Fetch(r.Context(), call.FetchRoute, rawURL)
	call.Route = call.FetchRoute
	if err == nil && result == nil {
		err = fmt.Errorf("empty fetch result")
	}
	s.recordCall(r, call, logstore.Entry{
		Protocol: "mcp",
		URL:      rawURL,
		Channels: meta.Channels,
		Mode:     meta.Mode,
	}, start, err)
	if err != nil {
		return mcpTextResult("fetch failed: "+err.Error(), true)
	}
	payload := map[string]any{
		"url":          result.URL,
		"title":        result.Title,
		"content":      result.Content,
		"content_type": result.ContentType,
		"status_code":  result.StatusCode,
		"route":        meta.RouteName,
		"mode":         meta.Mode,
		"channels":     meta.Channels,
	}
	return mcpJSONResult(payload)
}

func mcpJSONResult(value any) any {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return mcpTextResult("encode result: "+err.Error(), true)
	}
	return mcpTextResult(string(data), false)
}

func mcpTextResult(text string, isError bool) map[string]any {
	result := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
	if isError {
		result["isError"] = true
	}
	return result
}

func writeMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      mcpResponseID(id),
		"result":  result,
	})
}

func writeMCPRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeJSON(w, status, map[string]any{
		"jsonrpc": "2.0",
		"id":      mcpResponseID(id),
		"error": mcpRPCError{
			Code:    code,
			Message: message,
		},
	})
}

func mcpResponseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func routeUsable(route store.Route) bool {
	return route.ID != "" && route.Enabled
}
