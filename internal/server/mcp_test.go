package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func mcpCall(t *testing.T, baseURL, token, method string, id any, params any) (int, map[string]any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		payload["id"] = id
	}
	if params != nil {
		payload["params"] = params
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if resp.StatusCode != http.StatusAccepted {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode MCP response: %v", err)
		}
	}
	return resp.StatusCode, out
}

func TestMCPHandshakeAndTools(t *testing.T) {
	sx := fakeSearxng(t, "mcp")
	page := fakeHTML(t)
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	status, init := mcpCall(t, ts.URL, legacyToken, "initialize", 1, map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d", status)
	}
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocol version = %v", result["protocolVersion"])
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "search-gateway" {
		t.Fatalf("server info = %+v", serverInfo)
	}

	status, _ = mcpCall(t, ts.URL, legacyToken, "notifications/initialized", nil, nil)
	if status != http.StatusAccepted {
		t.Fatalf("initialized notification status = %d", status)
	}

	if status, discover := mcpCall(t, ts.URL, legacyToken, "server/discover", "probe", nil); status != http.StatusOK {
		t.Fatalf("discover status = %d", status)
	} else if rpcErr, _ := discover["error"].(map[string]any); rpcErr["code"] != float64(-32601) {
		t.Fatalf("discover should signal legacy fallback: %+v", discover)
	}

	status, listed := mcpCall(t, ts.URL, legacyToken, "tools/list", 2, nil)
	if status != http.StatusOK {
		t.Fatalf("tools/list status = %d", status)
	}
	listResult, _ := listed["result"].(map[string]any)
	tools, _ := listResult["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		names = append(names, fmt.Sprint(tool["name"]))
	}
	if strings.Join(names, ",") != "web_search,web_fetch" {
		t.Fatalf("tools = %v", names)
	}

	status, searchOut := mcpCall(t, ts.URL, legacyToken, "tools/call", 3, map[string]any{
		"name":      "web_search",
		"arguments": map[string]any{"query": "hello", "count": 2},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call search status = %d", status)
	}
	searchText := mcpContentText(t, searchOut)
	if !strings.Contains(searchText, "example.com/mcp") || !strings.Contains(searchText, `"results"`) {
		t.Fatalf("search tool result = %s", searchText)
	}

	status, fetchOut := mcpCall(t, ts.URL, legacyToken, "tools/call", 4, map[string]any{
		"name":      "web_fetch",
		"arguments": map[string]any{"url": page.URL},
	})
	if status != http.StatusOK {
		t.Fatalf("tools/call fetch status = %d", status)
	}
	fetchText := mcpContentText(t, fetchOut)
	if !strings.Contains(fetchText, "Test Page") || !strings.Contains(fetchText, "Hello world") {
		t.Fatalf("fetch tool result = %s", fetchText)
	}

	_, logData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/logs?protocol=mcp", adminToken, "")
	entries, _ := decodeMap(t, logData)["entries"].([]any)
	if len(entries) < 2 {
		t.Fatalf("MCP calls not logged: %s", logData)
	}
}

func mcpContentText(t *testing.T, out map[string]any) string {
	t.Helper()
	result, _ := out["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("missing MCP content: %+v", out)
	}
	block, _ := content[0].(map[string]any)
	return fmt.Sprint(block["text"])
}

func TestMCPRequiresKey(t *testing.T) {
	sx := fakeSearxng(t, "mcp")
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	status, _ := mcpCall(t, ts.URL, "", "initialize", 1, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP status = %d", status)
	}
}
