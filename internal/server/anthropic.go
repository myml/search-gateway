package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"search-gateway/internal/search"
)

// anthropicRequest is the subset of the Anthropic Messages API this gateway reads.
type anthropicRequest struct {
	Model    string             `json:"model"`
	Stream   bool               `json:"stream"`
	Messages []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// POST /v1/messages — search-only Anthropic Messages endpoint.
//
// It never calls an LLM: it extracts the search query from the last user text
// (DSH sends "Perform a web search for the query: <q>"), runs the route's
// search and returns Anthropic-style server-tool blocks.
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	var in anthropicRequest
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if in.Stream {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
			"streaming is not supported by this search gateway; send stream=false")
		return
	}
	query, err := extractQuery(in.Messages)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	results, meta, err := s.runSearch(r, search.SearchRequest{
		Query:            query,
		Count:            s.cfg.DefaultCount,
		OriginalQuestion: query,
	}, "messages", query)
	if err != nil {
		s.writeAnthropicGatewayError(w, err)
		return
	}
	setRouteHeaders(w, meta)
	writeJSON(w, http.StatusOK, buildAnthropicResponse(in.Model, query, results))
}

func extractQuery(messages []anthropicMessage) (string, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		text := contentText(messages[i].Content)
		if text == "" {
			continue
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, "Perform a web search for the query:"))
		if text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("no user text message found")
}

func contentText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, block := range blocks {
			if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
				return strings.TrimSpace(block.Text)
			}
		}
	}
	return ""
}

func buildAnthropicResponse(model, query string, results []search.Result) map[string]any {
	toolUseID := "srvtoolu_" + randomID(12)
	items := make([]any, 0, len(results))
	for _, r := range results {
		item := map[string]any{"type": "web_search_result", "url": r.URL}
		if r.Title != "" {
			item["title"] = r.Title
		}
		if r.Date != "" {
			item["page_age"] = r.Date
		}
		items = append(items, item)
	}

	content := []any{
		map[string]any{
			"type":  "server_tool_use",
			"id":    toolUseID,
			"name":  "web_search",
			"input": map[string]any{"query": query},
		},
		map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": toolUseID,
			"content":     items,
		},
	}

	if len(results) > 0 {
		citations := make([]any, 0, len(results))
		for _, r := range results {
			citation := map[string]any{
				"type": "web_search_result_location",
				"url":  r.URL,
			}
			if r.Title != "" {
				citation["title"] = r.Title
			}
			if r.Snippet != "" {
				citation["cited_text"] = r.Snippet
			}
			citations = append(citations, citation)
		}
		content = append(content, map[string]any{
			"type":      "text",
			"text":      fmt.Sprintf("Found %d web result(s) for %q.", len(results), query),
			"citations": citations,
		})
	}

	if model == "" {
		model = "search-gateway"
	}
	return map[string]any{
		"id":            "msg_" + randomID(12),
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}
