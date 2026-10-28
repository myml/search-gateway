package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/routing"
	"search-gateway/internal/server"
	"search-gateway/internal/store"
)

const (
	legacyToken = "legacy-token"
	adminToken  = "admin-token"
)

func loadTestConfig(t *testing.T, raw string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func newTestStack(t *testing.T, raw string) (*httptest.Server, *config.Config) {
	t.Helper()
	ts, cfg, _ := newTestStackWithDB(t, raw)
	return ts, cfg
}

// newTestStackWithDB additionally exposes the SQLite handle, so tests can
// assert on quota rows that are not visible through the admin API.
func newTestStackWithDB(t *testing.T, raw string) (*httptest.Server, *config.Config, *db.DB) {
	t.Helper()
	cfg := loadTestConfig(t, raw)
	dir := t.TempDir()
	cfg.StorePath = filepath.Join(dir, "store.json")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(cfg.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(dir, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine := routing.New(st, cfg, database, logger)
	ts := httptest.NewServer(server.New(cfg, st, engine, database, logger).Handler())
	t.Cleanup(func() {
		ts.Close()
		database.Close()
	})
	return ts, cfg, database
}

func fakeSearxng(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"url": "https://example.com/" + tag, "title": tag, "content": "snippet " + tag, "publishedDate": "2026-01-01T00:00:00Z"},
				{"url": "https://example.com/shared", "title": "shared"},
			},
		})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func fakeHTML(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Test Page</title><script>bad()</script></head>
<body><p>Hello world paragraph.</p></body></html>`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func doRequest(t *testing.T, method, url, token, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return out
}

func legacyConfig(t *testing.T, searxURL string) string {
	t.Helper()
	return fmt.Sprintf(`{
		"listen": "127.0.0.1:0",
		"auth": {"tokens": [%q]},
		"admin_tokens": [%q],
		"timeout_seconds": 5,
		"fetch_timeout_seconds": 5,
		"search": [{"name": "sx-a", "type": "searxng", "base_url": %q}],
		"fetch": [{"name": "direct", "type": "direct"}]
	}`, legacyToken, adminToken, searxURL)
}

func TestGatewaySearchAndLogs(t *testing.T) {
	sx := fakeSearxng(t, "a")
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	resp, data := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", legacyToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search: %d %s", resp.StatusCode, data)
	}
	if got := resp.Header.Get("X-Search-Route"); got != "default" {
		t.Fatalf("route header = %q", got)
	}
	out := decodeMap(t, data)
	results, _ := out["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %s", data)
	}

	infoResp, infoData := doRequest(t, http.MethodGet, ts.URL+"/api/info", legacyToken, "")
	if infoResp.StatusCode != http.StatusOK {
		t.Fatalf("info: %d %s", infoResp.StatusCode, infoData)
	}
	info := decodeMap(t, infoData)
	route, _ := info["route"].(map[string]any)
	if route["name"] != "default" {
		t.Fatalf("info route = %s", infoData)
	}

	logResp, logData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/logs?protocol=search", adminToken, "")
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("logs: %d %s", logResp.StatusCode, logData)
	}
	logsOut := decodeMap(t, logData)
	entries, _ := logsOut["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("no search log entries: %s", logData)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["query"] != "hello" || entry["route_name"] != "default" || entry["api_key_name"] != "legacy-1" {
		t.Fatalf("unexpected log entry: %+v", entry)
	}
	if entry["status"] != float64(200) {
		t.Fatalf("status = %v", entry["status"])
	}
}

func TestAuthFailureLogged(t *testing.T) {
	sx := fakeSearxng(t, "a")
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/search?q=x", "wrong-key", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_, data := doRequest(t, http.MethodGet, ts.URL+"/admin/api/logs?protocol=auth", adminToken, "")
	entries, _ := decodeMap(t, data)["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("auth failure not logged: %s", data)
	}
}

func TestAdminRoutingAndMerge(t *testing.T) {
	sx := fakeSearxng(t, "a")
	sx2 := fakeSearxng(t, "b")
	page := fakeHTML(t)
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	if resp, _ := doRequest(t, http.MethodGet, ts.URL+"/admin/api/overview", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin without token = %d", resp.StatusCode)
	}
	_, overviewData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/overview", adminToken, "")
	overview := decodeMap(t, overviewData)
	channels, _ := overview["channels"].([]any)
	var channelAID string
	for _, raw := range channels {
		ch, _ := raw.(map[string]any)
		if ch["name"] == "sx-a" {
			channelAID, _ = ch["id"].(string)
		}
	}
	if channelAID == "" {
		t.Fatalf("seeded channel missing: %s", overviewData)
	}

	createResp, createData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		fmt.Sprintf(`{"name":"sx-b","type":"searxng","enabled":true,"config":{"base_url":%q}}`, sx2.URL))
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create channel: %d %s", createResp.StatusCode, createData)
	}
	channelB := decodeMap(t, createData)
	channelBID, _ := channelB["id"].(string)

	routeResp, routeData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"only-b","mode":"fallback","enabled":true,"search_channels":[%q],"fetch_channels":[]}`, channelBID))
	if routeResp.StatusCode != http.StatusCreated {
		t.Fatalf("create route: %d %s", routeResp.StatusCode, routeData)
	}
	route := decodeMap(t, routeData)
	routeID, _ := route["id"].(string)

	keyResp, keyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-b","route_id":%q}`, routeID))
	if keyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create key: %d %s", keyResp.StatusCode, keyData)
	}
	keyOut := decodeMap(t, keyData)
	keyToken, _ := keyOut["token"].(string)
	if keyToken == "" {
		t.Fatalf("missing token: %s", keyData)
	}
	keyObj, _ := keyOut["key"].(map[string]any)
	keyID, _ := keyObj["id"].(string)

	// Admin key test runs through the key's route without knowing the plaintext.
	keyTestResp, keyTestData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys/"+keyID+"/test", adminToken, `{"query":"hello"}`)
	if keyTestResp.StatusCode != http.StatusOK {
		t.Fatalf("key test: %d %s", keyTestResp.StatusCode, keyTestData)
	}
	keyTestOut := decodeMap(t, keyTestData)
	if routeInfo, _ := keyTestOut["route"].(map[string]any); routeInfo["name"] != "only-b" {
		t.Fatalf("key test route = %s", keyTestData)
	}
	if results, _ := keyTestOut["results"].([]any); len(results) == 0 {
		t.Fatalf("key test results = %s", keyTestData)
	}
	if fetchResp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys/"+keyID+"/test", adminToken, `{"url":"https://example.com"}`); fetchResp.StatusCode != http.StatusConflict {
		t.Fatalf("key test fetch without fetch route = %d, want 409", fetchResp.StatusCode)
	}
	if emptyResp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys/"+keyID+"/test", adminToken, `{}`); emptyResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty key test = %d, want 400", emptyResp.StatusCode)
	}
	if missingResp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys/key-missing/test", adminToken, `{"query":"x"}`); missingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing key test = %d, want 404", missingResp.StatusCode)
	}
	if appendLogsResp, logsData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/logs?protocol=key-test", adminToken, ""); appendLogsResp.StatusCode != http.StatusOK {
		t.Fatalf("key-test logs: %d %s", appendLogsResp.StatusCode, logsData)
	} else if logsOut := decodeMap(t, logsData); logsOut["total"].(float64) < 1 {
		t.Fatalf("key-test log missing: %s", logsData)
	}

	// A disabled key cannot be tested.
	if toggleResp, _ := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/keys/"+keyID, adminToken, `{"enabled":false}`); toggleResp.StatusCode != http.StatusOK {
		t.Fatalf("disable key: %d", toggleResp.StatusCode)
	}
	if disabledResp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys/"+keyID+"/test", adminToken, `{"query":"hello"}`); disabledResp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled key test = %d, want 409", disabledResp.StatusCode)
	}
	if toggleResp, _ := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/keys/"+keyID, adminToken, `{"enabled":true}`); toggleResp.StatusCode != http.StatusOK {
		t.Fatalf("re-enable key: %d", toggleResp.StatusCode)
	}

	searchResp, searchData := doRequest(t, http.MethodGet, ts.URL+"/search?q=x", keyToken, "")
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("key-b search: %d %s", searchResp.StatusCode, searchData)
	}
	if got := searchResp.Header.Get("X-Search-Route"); got != "only-b" {
		t.Fatalf("route header = %q", got)
	}
	searchOut := decodeMap(t, searchData)
	results, _ := searchOut["results"].([]any)
	first, _ := results[0].(map[string]any)
	if first["title"] != "b" {
		t.Fatalf("expected sx-b result, got %s", searchData)
	}

	// Merge mode deduplicates the shared URL.
	mergeResp, mergeData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"merged","mode":"merge","enabled":true,"search_channels":[%q,%q],"fetch_channels":[]}`, channelAID, channelBID))
	if mergeResp.StatusCode != http.StatusCreated {
		t.Fatalf("create merge route: %d %s", mergeResp.StatusCode, mergeData)
	}
	mergeRoute := decodeMap(t, mergeData)
	mergeRouteID, _ := mergeRoute["id"].(string)
	mergeKeyResp, mergeKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-merge","route_id":%q}`, mergeRouteID))
	if mergeKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create merge key: %d %s", mergeKeyResp.StatusCode, mergeKeyData)
	}
	mergeToken, _ := decodeMap(t, mergeKeyData)["token"].(string)
	_, mergedData := doRequest(t, http.MethodGet, ts.URL+"/search?q=x&count=10", mergeToken, "")
	mergedResults, _ := decodeMap(t, mergedData)["results"].([]any)
	if len(mergedResults) != 3 {
		t.Fatalf("merged results = %d, want 3 (shared URL deduped): %s", len(mergedResults), mergedData)
	}

	// Balance mode rotates the starting channel per request.
	balanceResp, balanceData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"balanced","mode":"balance","balance":"round_robin","enabled":true,"search_channels":[%q,%q],"fetch_channels":[]}`, channelAID, channelBID))
	if balanceResp.StatusCode != http.StatusCreated {
		t.Fatalf("create balance route: %d %s", balanceResp.StatusCode, balanceData)
	}
	balanceRoute := decodeMap(t, balanceData)
	if balanceRoute["balance"] != "round_robin" {
		t.Fatalf("balance route = %s", balanceData)
	}
	balanceKeyResp, balanceKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-balance","route_id":%q}`, balanceRoute["id"]))
	if balanceKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create balance key: %d %s", balanceKeyResp.StatusCode, balanceKeyData)
	}
	balanceToken, _ := decodeMap(t, balanceKeyData)["token"].(string)
	var titles []string
	for i := 0; i < 4; i++ {
		_, searchData := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", balanceToken, "")
		results, _ := decodeMap(t, searchData)["results"].([]any)
		first, _ := results[0].(map[string]any)
		titles = append(titles, fmt.Sprint(first["title"]))
	}
	want := []string{"a", "b", "a", "b"}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("round robin titles = %v, want %v", titles, want)
		}
	}
	if resp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"bad-balance","mode":"balance","balance":"weighted","enabled":true,"search_channels":[%q]}`, channelAID)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid balance strategy = %d, want 400", resp.StatusCode)
	}

	// Nested groups: a balance parent over fallback child routes.
	groupAResp, groupAData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"group-a","mode":"fallback","enabled":true,"search_channels":[%q],"fetch_channels":[]}`, channelAID))
	if groupAResp.StatusCode != http.StatusCreated {
		t.Fatalf("create group-a: %d %s", groupAResp.StatusCode, groupAData)
	}
	groupAID, _ := decodeMap(t, groupAData)["id"].(string)
	groupBResp, groupBData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"group-b","mode":"fallback","enabled":true,"search_channels":[%q],"fetch_channels":[]}`, channelBID))
	if groupBResp.StatusCode != http.StatusCreated {
		t.Fatalf("create group-b: %d %s", groupBResp.StatusCode, groupBData)
	}
	groupBID, _ := decodeMap(t, groupBData)["id"].(string)
	nestedResp, nestedData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"nested-balance","mode":"balance","balance":"round_robin","enabled":true,"search_channels":[],"fetch_channels":[],"search_groups":[%q,%q]}`, groupAID, groupBID))
	if nestedResp.StatusCode != http.StatusCreated {
		t.Fatalf("create nested route: %d %s", nestedResp.StatusCode, nestedData)
	}
	nestedRoute := decodeMap(t, nestedData)
	nestedKeyResp, nestedKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-nested","route_id":%q}`, nestedRoute["id"]))
	if nestedKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create nested key: %d %s", nestedKeyResp.StatusCode, nestedKeyData)
	}
	nestedToken, _ := decodeMap(t, nestedKeyData)["token"].(string)
	var nestedTitles []string
	for i := 0; i < 4; i++ {
		_, searchData := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", nestedToken, "")
		results, _ := decodeMap(t, searchData)["results"].([]any)
		first, _ := results[0].(map[string]any)
		nestedTitles = append(nestedTitles, fmt.Sprint(first["title"]))
	}
	for i := range want {
		if nestedTitles[i] != want[i] {
			t.Fatalf("nested group titles = %v, want %v", nestedTitles, want)
		}
	}

	// A failing group falls back to the next group.
	badChResp, badChData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		`{"name":"sx-bad","type":"searxng","enabled":true,"config":{"base_url":"http://127.0.0.1:1"}}`)
	if badChResp.StatusCode != http.StatusCreated {
		t.Fatalf("create bad channel: %d %s", badChResp.StatusCode, badChData)
	}
	badChID, _ := decodeMap(t, badChData)["id"].(string)
	badGroupResp, badGroupData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"group-bad","mode":"fallback","enabled":true,"search_channels":[%q],"fetch_channels":[]}`, badChID))
	if badGroupResp.StatusCode != http.StatusCreated {
		t.Fatalf("create bad group: %d %s", badGroupResp.StatusCode, badGroupData)
	}
	badGroupID, _ := decodeMap(t, badGroupData)["id"].(string)
	fallbackGroupsResp, fallbackGroupsData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"nested-fallback","mode":"fallback","enabled":true,"search_channels":[],"fetch_channels":[],"search_groups":[%q,%q]}`, badGroupID, groupBID))
	if fallbackGroupsResp.StatusCode != http.StatusCreated {
		t.Fatalf("create nested fallback: %d %s", fallbackGroupsResp.StatusCode, fallbackGroupsData)
	}
	fallbackGroupsRoute := decodeMap(t, fallbackGroupsData)
	fbKeyResp, fbKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-nested-fb","route_id":%q}`, fallbackGroupsRoute["id"]))
	if fbKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create nested fallback key: %d %s", fbKeyResp.StatusCode, fbKeyData)
	}
	fbToken, _ := decodeMap(t, fbKeyData)["token"].(string)
	_, fbSearchData := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", fbToken, "")
	fbResults, _ := decodeMap(t, fbSearchData)["results"].([]any)
	if len(fbResults) == 0 {
		t.Fatalf("nested fallback results = %s", fbSearchData)
	}
	firstFB, _ := fbResults[0].(map[string]any)
	if firstFB["title"] != "b" {
		t.Fatalf("nested fallback title = %v", firstFB["title"])
	}
	// max_retries limits how many targets are attempted after the first.
	bad2Resp, bad2Data := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		`{"name":"sx-bad2","type":"searxng","enabled":true,"config":{"base_url":"http://127.0.0.1:2"}}`)
	if bad2Resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second bad channel: %d %s", bad2Resp.StatusCode, bad2Data)
	}
	bad2ID, _ := decodeMap(t, bad2Data)["id"].(string)
	badGroup2Resp, badGroup2Data := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"group-bad-2","role":"search","mode":"fallback","enabled":true,"search_channels":[%q],"fetch_channels":[]}`, bad2ID))
	if badGroup2Resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second bad group: %d %s", badGroup2Resp.StatusCode, badGroup2Data)
	}
	badGroup2ID, _ := decodeMap(t, badGroup2Data)["id"].(string)
	retryRouteResp, retryRouteData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"retry-one","role":"search","mode":"fallback","max_retries":1,"enabled":true,"search_channels":[],"fetch_channels":[],"search_groups":[%q,%q,%q]}`, badGroupID, badGroup2ID, groupBID))
	if retryRouteResp.StatusCode != http.StatusCreated {
		t.Fatalf("create retry-one route: %d %s", retryRouteResp.StatusCode, retryRouteData)
	}
	retryRouteID, _ := decodeMap(t, retryRouteData)["id"].(string)
	retryKeyResp, retryKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-retry-one","search_route_id":%q}`, retryRouteID))
	if retryKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create retry-one key: %d %s", retryKeyResp.StatusCode, retryKeyData)
	}
	retryToken, _ := decodeMap(t, retryKeyData)["token"].(string)
	if resp, _ := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", retryToken, ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("retry-one (2 attempts, 3 targets) = %d, want 502", resp.StatusCode)
	}
	retryTwoResp, retryTwoData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"retry-two","role":"search","mode":"fallback","max_retries":2,"enabled":true,"search_channels":[],"fetch_channels":[],"search_groups":[%q,%q,%q]}`, badGroupID, badGroup2ID, groupBID))
	if retryTwoResp.StatusCode != http.StatusCreated {
		t.Fatalf("create retry-two route: %d %s", retryTwoResp.StatusCode, retryTwoData)
	}
	retryTwoID, _ := decodeMap(t, retryTwoData)["id"].(string)
	retryTwoKeyResp, retryTwoKeyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-retry-two","search_route_id":%q}`, retryTwoID))
	if retryTwoKeyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create retry-two key: %d %s", retryTwoKeyResp.StatusCode, retryTwoKeyData)
	}
	retryTwoToken, _ := decodeMap(t, retryTwoKeyData)["token"].(string)
	_, retryTwoSearch := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", retryTwoToken, "")
	retryTwoResults, _ := decodeMap(t, retryTwoSearch)["results"].([]any)
	if len(retryTwoResults) == 0 {
		t.Fatalf("retry-two results = %s", retryTwoSearch)
	}
	if first, _ := retryTwoResults[0].(map[string]any); first["title"] != "b" {
		t.Fatalf("retry-two title = %v", first["title"])
	}

	// Remove the intentionally broken channel so later batch-level assertions
	// operate on the original healthy channel set.
	for _, id := range []string{badChID, bad2ID} {
		if resp, _ := doRequest(t, http.MethodDelete, ts.URL+"/admin/api/channels/"+id, adminToken, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete bad channel %s = %d", id, resp.StatusCode)
		}
	}

	// Route deletion is blocked while a key references it.
	if resp, _ := doRequest(t, http.MethodDelete, ts.URL+"/admin/api/routes/"+routeID, adminToken, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete referenced route = %d, want 409", resp.StatusCode)
	}

	// Channel test endpoint records a visible check.
	testResp, testData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/"+channelBID+"/test", adminToken, `{"query":"hello"}`)
	if testResp.StatusCode != http.StatusOK {
		t.Fatalf("channel test: %d %s", testResp.StatusCode, testData)
	}
	testOut := decodeMap(t, testData)
	check, _ := testOut["check"].(map[string]any)
	if check["ok"] != true || check["kind"] != "search" || check["result_count"] != float64(2) {
		t.Fatalf("channel test check = %s", testData)
	}

	_, listData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/channels", adminToken, "")
	var tested map[string]any
	for _, raw := range decodeMap(t, listData)["channels"].([]any) {
		channel, _ := raw.(map[string]any)
		if channel["name"] == "sx-b" {
			tests, _ := channel["tests"].(map[string]any)
			tested, _ = tests["search"].(map[string]any)
		}
	}
	if tested == nil || tested["ok"] != true || tested["latency_ms"] == nil {
		t.Fatalf("channel test status not exposed: %s", listData)
	}

	// Test every channel concurrently; the direct fetch channel uses the URL.
	batchResp, batchData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/test", adminToken,
		fmt.Sprintf(`{"query": "hello", "url": %q}`, page.URL))
	if batchResp.StatusCode != http.StatusOK {
		t.Fatalf("batch test: %d %s", batchResp.StatusCode, batchData)
	}
	batch := decodeMap(t, batchData)
	results, _ = batch["results"].([]any)
	if len(results) != 3 || batch["ok_count"] != float64(3) || batch["fail_count"] != float64(0) {
		t.Fatalf("batch results = %s", batchData)
	}
	var directCheck map[string]any
	for _, raw := range results {
		item, _ := raw.(map[string]any)
		if item["name"] == "direct" {
			directCheck, _ = item["check"].(map[string]any)
		}
	}
	if directCheck == nil || directCheck["kind"] != "fetch" || directCheck["content_chars"] == nil || directCheck["content_chars"] == float64(0) {
		t.Fatalf("direct fetch check = %s", batchData)
	}

	// Role-scoped batch tests only touch channels for that role.
	searchBatchResp, searchBatchData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/test", adminToken,
		`{"role": "search", "query": "hello"}`)
	if searchBatchResp.StatusCode != http.StatusOK {
		t.Fatalf("search batch: %d %s", searchBatchResp.StatusCode, searchBatchData)
	}
	searchBatch := decodeMap(t, searchBatchData)
	if searchBatch["ok_count"] != float64(2) || len(searchBatch["results"].([]any)) != 2 {
		t.Fatalf("search batch = %s", searchBatchData)
	}
	fetchBatchResp, fetchBatchData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/test", adminToken,
		fmt.Sprintf(`{"role": "fetch", "url": %q}`, page.URL))
	if fetchBatchResp.StatusCode != http.StatusOK {
		t.Fatalf("fetch batch: %d %s", fetchBatchResp.StatusCode, fetchBatchData)
	}
	fetchBatch := decodeMap(t, fetchBatchData)
	fetchResults, _ := fetchBatch["results"].([]any)
	if fetchBatch["ok_count"] != float64(1) || len(fetchResults) != 1 {
		t.Fatalf("fetch batch = %s", fetchBatchData)
	}
	fetchCheck, _ := fetchResults[0].(map[string]any)["check"].(map[string]any)
	if fetchCheck["kind"] != "fetch" || fetchCheck["content_chars"] == float64(0) {
		t.Fatalf("fetch check = %s", fetchBatchData)
	}
	if resp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/test", adminToken, `{"role": "nope"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad role = %d, want 400", resp.StatusCode)
	}

	// Test calls are written to the call log as admin-test entries.
	logResp, logData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/logs?protocol=admin-test&limit=100", adminToken, "")
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("admin-test logs: %d %s", logResp.StatusCode, logData)
	}
	logsOut := decodeMap(t, logData)
	entries, _ := logsOut["entries"].([]any)
	if len(entries) < 6 { // one single test + five batch tests
		t.Fatalf("expected admin-test log entries, got %d", len(entries))
	}

	// Missing query/url is a bad request.
	if resp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels/"+channelBID+"/test", adminToken, `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty test = %d, want 400", resp.StatusCode)
	}

	// Invalid channel type is rejected.
	if resp, _ := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		`{"name":"bad","type":"nope","enabled":true,"config":{}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid type = %d, want 400", resp.StatusCode)
	}
}

func TestAnthropicAndFetch(t *testing.T) {
	sx := fakeSearxng(t, "a")
	page := fakeHTML(t)
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	body := `{
		"model": "deepseek-v4-flash",
		"stream": false,
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Perform a web search for the query: axonhub"}]}],
		"tools": [{"type": "web_search_20250305", "name": "web_search"}]
	}`
	resp, data := doRequest(t, http.MethodPost, ts.URL+"/v1/messages", legacyToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("messages: %d %s", resp.StatusCode, data)
	}
	content, _ := decodeMap(t, data)["content"].([]any)
	if len(content) < 2 {
		t.Fatalf("content = %s", data)
	}
	block, _ := content[1].(map[string]any)
	if block["type"] != "web_search_tool_result" {
		t.Fatalf("block = %+v", block)
	}

	fetchResp, fetchData := doRequest(t, http.MethodGet, ts.URL+"/fetch?url="+page.URL, legacyToken, "")
	if fetchResp.StatusCode != http.StatusOK {
		t.Fatalf("fetch: %d %s", fetchResp.StatusCode, fetchData)
	}
	fetchOut := decodeMap(t, fetchData)
	if fetchOut["title"] != "Test Page" || !strings.Contains(fmt.Sprint(fetchOut["content"]), "Hello world paragraph.") {
		t.Fatalf("fetch = %s", fetchData)
	}
}

func TestUIAndHealth(t *testing.T) {
	sx := fakeSearxng(t, "a")
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	resp, data := doRequest(t, http.MethodGet, ts.URL+"/", "", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("UI: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, want := range []string{"search-gateway", "/assets/app.js", "/assets/app.css"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("UI missing %q", want)
		}
	}
	jsResp, jsData := doRequest(t, http.MethodGet, ts.URL+"/assets/app.js", "", "")
	if jsResp.StatusCode != http.StatusOK || !strings.Contains(string(jsData), "/admin/api/overview") || !strings.Contains(string(jsData), "Admin Token") || !strings.Contains(string(jsData), "tutMCP") {
		t.Fatalf("app.js: %d", jsResp.StatusCode)
	}
	cssResp, cssData := doRequest(t, http.MethodGet, ts.URL+"/assets/app.css", "", "")
	if cssResp.StatusCode != http.StatusOK || !strings.Contains(cssResp.Header.Get("Content-Type"), "text/css") || !strings.Contains(string(cssData), "--accent") {
		t.Fatalf("app.css: %d %s", cssResp.StatusCode, cssResp.Header.Get("Content-Type"))
	}
	healthResp, healthData := doRequest(t, http.MethodGet, ts.URL+"/healthz", "", "")
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d %s", healthResp.StatusCode, healthData)
	}
	health := decodeMap(t, healthData)
	if health["channels"] != float64(2) || health["routes"] != float64(2) {
		t.Fatalf("health = %s", healthData)
	}
}

func TestUnsupportedChannelTypeAPI(t *testing.T) {
	sx := fakeSearxng(t, "a")
	ts, _ := newTestStack(t, legacyConfig(t, sx.URL))

	for _, payload := range []string{
		`{"name":"custom-fetch","type":"generic","enabled":true,"config":{"url":"https://api.example/fetch"}}`,
		`{"name":"custom-unknown","type":"nope","enabled":true}`,
	} {
		resp, data := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken, payload)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("create unsupported channel: %d %s", resp.StatusCode, data)
		}
	}

	_, listData := doRequest(t, http.MethodGet, ts.URL+"/admin/api/channels", adminToken, "")
	for _, raw := range decodeMap(t, listData)["channels"].([]any) {
		ch, _ := raw.(map[string]any)
		switch ch["type"] {
		case "generic", "nope":
			t.Fatalf("unsupported channel was created: %s", ch)
		}
	}
}

func listAdminChannels(t *testing.T, ts *httptest.Server) []any {
	t.Helper()
	resp, data := doRequest(t, http.MethodGet, ts.URL+"/admin/api/channels", adminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list channels: %d %s", resp.StatusCode, data)
	}
	channels, _ := decodeMap(t, data)["channels"].([]any)
	return channels
}

func channelByName(t *testing.T, ts *httptest.Server, name string) map[string]any {
	t.Helper()
	for _, raw := range listAdminChannels(t, ts) {
		ch, _ := raw.(map[string]any)
		if ch["name"] == name {
			return ch
		}
	}
	t.Fatalf("channel %q not found", name)
	return nil
}

func overviewChannel(t *testing.T, ts *httptest.Server, name string) map[string]any {
	t.Helper()
	resp, data := doRequest(t, http.MethodGet, ts.URL+"/admin/api/overview", adminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview: %d %s", resp.StatusCode, data)
	}
	for _, raw := range decodeMap(t, data)["channels"].([]any) {
		ch, _ := raw.(map[string]any)
		if ch["name"] == name {
			return ch
		}
	}
	t.Fatalf("channel %q missing from overview: %s", name, data)
	return nil
}

func channelQuota(t *testing.T, ch map[string]any) map[string]any {
	t.Helper()
	quota, _ := ch["quota"].(map[string]any)
	if quota == nil {
		t.Fatalf("channel %v has no quota object: %v", ch["name"], ch)
	}
	return quota
}

func assertQuotaNumber(t *testing.T, quota map[string]any, field string, want float64) {
	t.Helper()
	if got := quota[field]; got != want {
		t.Fatalf("quota.%s = %v, want %v (quota: %v)", field, got, want, quota)
	}
}

func TestChannelQuotaAdminAPI(t *testing.T) {
	sxA := fakeSearxng(t, "a")
	sxB := fakeSearxng(t, "b")
	ts, cfg, database := newTestStackWithDB(t, legacyConfig(t, sxA.URL))
	period := cfg.QuotaPeriod(time.Now())

	// The seeded channel exposes a quota object: unlimited and zero usage.
	channelA := channelByName(t, ts, "sx-a")
	channelAID, _ := channelA["id"].(string)
	quotaA := channelQuota(t, channelA)
	assertQuotaNumber(t, quotaA, "monthly_limit", 0)
	assertQuotaNumber(t, quotaA, "used", 0)
	assertQuotaNumber(t, quotaA, "errors", 0)
	assertQuotaNumber(t, quotaA, "remaining", 0)
	if quotaA["unlimited"] != true || quotaA["exhausted"] != false {
		t.Fatalf("initial quota flags = %v", quotaA)
	}
	if quotaA["period"] != period {
		t.Fatalf("quota period = %v, want %q", quotaA["period"], period)
	}
	resetsRaw, _ := quotaA["resets_at"].(string)
	resetsAt, err := time.Parse(time.RFC3339, resetsRaw)
	if err != nil {
		t.Fatalf("resets_at %q is not RFC3339: %v", resetsRaw, err)
	}
	_, wantResetsAt := cfg.QuotaPeriodBounds(time.Now())
	if !resetsAt.Equal(wantResetsAt) {
		t.Fatalf("resets_at = %v, want %v", resetsAt, wantResetsAt)
	}

	// POST without monthly_quota defaults to unlimited.
	createResp, createData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		fmt.Sprintf(`{"name":"sx-b","type":"searxng","enabled":true,"config":{"base_url":%q}}`, sxB.URL))
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create sx-b: %d %s", createResp.StatusCode, createData)
	}
	createdB := decodeMap(t, createData)
	channelBID, _ := createdB["id"].(string)
	assertQuotaNumber(t, channelQuota(t, createdB), "monthly_limit", 0)

	// A partial PATCH that only carries monthly_quota updates nothing else.
	patchResp, patchData := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/channels/"+channelBID, adminToken,
		`{"monthly_quota":1}`)
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("partial quota patch: %d %s", patchResp.StatusCode, patchData)
	}
	patched := decodeMap(t, patchData)
	if patched["name"] != "sx-b" || patched["type"] != "searxng" || patched["enabled"] != true {
		t.Fatalf("partial patch changed identity fields: %s", patchData)
	}
	patchedConfig, _ := patched["config"].(map[string]any)
	if patchedConfig["base_url"] != sxB.URL {
		t.Fatalf("partial patch changed config: %s", patchData)
	}
	patchedQuota := channelQuota(t, patched)
	assertQuotaNumber(t, patchedQuota, "monthly_limit", 1)
	assertQuotaNumber(t, patchedQuota, "remaining", 1)
	if patchedQuota["unlimited"] != false || patchedQuota["exhausted"] != false {
		t.Fatalf("patched quota flags = %v", patchedQuota)
	}
	assertQuotaNumber(t, channelQuota(t, channelByName(t, ts, "sx-b")), "monthly_limit", 1)

	// An old client toggling the channel (full config, no monthly_quota) must
	// neither wipe the quota nor the stored config.
	toggleResp, toggleData := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/channels/"+channelBID, adminToken,
		fmt.Sprintf(`{"name":"sx-b","type":"searxng","enabled":false,"config":{"base_url":%q}}`, sxB.URL))
	if toggleResp.StatusCode != http.StatusOK {
		t.Fatalf("toggle patch: %d %s", toggleResp.StatusCode, toggleData)
	}
	toggled := decodeMap(t, toggleData)
	if toggled["enabled"] != false {
		t.Fatalf("toggle did not disable channel: %s", toggleData)
	}
	assertQuotaNumber(t, channelQuota(t, toggled), "monthly_limit", 1)
	toggledConfig, _ := toggled["config"].(map[string]any)
	if toggledConfig["base_url"] != sxB.URL {
		t.Fatalf("toggle patch lost config: %s", toggleData)
	}
	if resp, data := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/channels/"+channelBID, adminToken,
		`{"enabled":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("re-enable channel: %d %s", resp.StatusCode, data)
	}

	// Negative quotas are rejected and leave the stored value untouched.
	if resp, data := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/channels/"+channelBID, adminToken,
		`{"monthly_quota":-1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative quota patch = %d, want 400: %s", resp.StatusCode, data)
	}
	if resp, data := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		fmt.Sprintf(`{"name":"neg","type":"searxng","config":{"base_url":%q},"monthly_quota":-1}`, sxB.URL)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative quota create = %d, want 400: %s", resp.StatusCode, data)
	}
	assertQuotaNumber(t, channelQuota(t, channelByName(t, ts, "sx-b")), "monthly_limit", 1)

	// Give sx-a a limit, use one call, then verify usage/remaining in both
	// overview and the channel list.
	if resp, data := doRequest(t, http.MethodPatch, ts.URL+"/admin/api/channels/"+channelAID, adminToken,
		`{"monthly_quota":5}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("set sx-a quota: %d %s", resp.StatusCode, data)
	}
	searchResp, searchData := doRequest(t, http.MethodGet, ts.URL+"/search?q=quota&count=2", legacyToken, "")
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("search for quota usage: %d %s", searchResp.StatusCode, searchData)
	}
	for _, ch := range []map[string]any{overviewChannel(t, ts, "sx-a"), channelByName(t, ts, "sx-a")} {
		quota := channelQuota(t, ch)
		assertQuotaNumber(t, quota, "monthly_limit", 5)
		assertQuotaNumber(t, quota, "used", 1)
		assertQuotaNumber(t, quota, "errors", 0)
		assertQuotaNumber(t, quota, "remaining", 4)
		if quota["unlimited"] != false || quota["exhausted"] != false {
			t.Fatalf("sx-a quota flags = %v", quota)
		}
	}

	// Resetting the current period zeroes the usage of that channel.
	resetResp, resetData := doRequest(t, http.MethodPost,
		ts.URL+"/admin/api/channels/"+channelAID+"/quota/reset", adminToken, "")
	if resetResp.StatusCode != http.StatusOK {
		t.Fatalf("reset quota: %d %s", resetResp.StatusCode, resetData)
	}
	resetOut := decodeMap(t, resetData)
	if resetOut["ok"] != true {
		t.Fatalf("reset response = %s", resetData)
	}
	resetQuota, _ := resetOut["quota"].(map[string]any)
	assertQuotaNumber(t, resetQuota, "used", 0)
	assertQuotaNumber(t, resetQuota, "remaining", 5)
	if resetQuota["exhausted"] != false {
		t.Fatalf("reset quota flags = %v", resetQuota)
	}
	if resp, _ := doRequest(t, http.MethodPost,
		ts.URL+"/admin/api/channels/ch-missing/quota/reset", adminToken, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("reset missing channel = %d, want 404", resp.StatusCode)
	}

	// Usage is counted again after the reset, and deleting the channel removes
	// its quota rows from SQLite.
	if resp, data := doRequest(t, http.MethodGet, ts.URL+"/search?q=quota2&count=2", legacyToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("second search: %d %s", resp.StatusCode, data)
	}
	usage, err := database.ChannelUsage(period)
	if err != nil {
		t.Fatalf("channel usage: %v", err)
	}
	if got := usage[channelAID].Requests; got != 1 {
		t.Fatalf("sx-a usage before delete = %d, want 1", got)
	}
	if resp, data := doRequest(t, http.MethodDelete, ts.URL+"/admin/api/channels/"+channelAID, adminToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete channel: %d %s", resp.StatusCode, data)
	}
	usage, err = database.ChannelUsage(period)
	if err != nil {
		t.Fatalf("channel usage after delete: %v", err)
	}
	if _, ok := usage[channelAID]; ok {
		t.Fatalf("channel usage row survived deletion: %+v", usage)
	}
	for _, raw := range listAdminChannels(t, ts) {
		if ch, _ := raw.(map[string]any); ch["id"] == channelAID {
			t.Fatalf("deleted channel still listed: %v", ch)
		}
	}
}

func TestQuotaExceededSearchReturns503(t *testing.T) {
	sxA := fakeSearxng(t, "a")
	sxB := fakeSearxng(t, "b")
	ts, _ := newTestStack(t, legacyConfig(t, sxA.URL))

	// A dedicated channel with a quota of one call, on its own route.
	createResp, createData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/channels", adminToken,
		fmt.Sprintf(`{"name":"sx-b","type":"searxng","enabled":true,"config":{"base_url":%q},"monthly_quota":1}`, sxB.URL))
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create sx-b: %d %s", createResp.StatusCode, createData)
	}
	channelBID, _ := decodeMap(t, createData)["id"].(string)
	assertQuotaNumber(t, channelQuota(t, decodeMap(t, createData)), "monthly_limit", 1)

	routeResp, routeData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/routes", adminToken,
		fmt.Sprintf(`{"name":"quota-b","mode":"fallback","enabled":true,"search_channels":[%q]}`, channelBID))
	if routeResp.StatusCode != http.StatusCreated {
		t.Fatalf("create quota route: %d %s", routeResp.StatusCode, routeData)
	}
	routeID, _ := decodeMap(t, routeData)["id"].(string)
	keyResp, keyData := doRequest(t, http.MethodPost, ts.URL+"/admin/api/keys", adminToken,
		fmt.Sprintf(`{"name":"key-quota","route_id":%q}`, routeID))
	if keyResp.StatusCode != http.StatusCreated {
		t.Fatalf("create quota key: %d %s", keyResp.StatusCode, keyData)
	}
	token, _ := decodeMap(t, keyData)["token"].(string)

	firstResp, firstData := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", token, "")
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first quota search: %d %s", firstResp.StatusCode, firstData)
	}
	quota := channelQuota(t, overviewChannel(t, ts, "sx-b"))
	assertQuotaNumber(t, quota, "used", 1)
	assertQuotaNumber(t, quota, "remaining", 0)
	if quota["exhausted"] != true || quota["unlimited"] != false {
		t.Fatalf("exhausted quota = %v", quota)
	}

	secondResp, secondData := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", token, "")
	if secondResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("exhausted search = %d, want 503: %s", secondResp.StatusCode, secondData)
	}
	errObj, _ := decodeMap(t, secondData)["error"].(map[string]any)
	message := fmt.Sprint(errObj["message"])
	if !strings.Contains(strings.ToLower(message), "quota") {
		t.Fatalf("503 message %q does not mention quota", message)
	}

	// Another channel with remaining quota still serves the same client traffic.
	if resp, data := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", legacyToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("unlimited channel search: %d %s", resp.StatusCode, data)
	}

	// Resetting the period makes the exhausted channel usable again.
	resetResp, resetData := doRequest(t, http.MethodPost,
		ts.URL+"/admin/api/channels/"+channelBID+"/quota/reset", adminToken, "")
	if resetResp.StatusCode != http.StatusOK {
		t.Fatalf("reset exhausted channel: %d %s", resetResp.StatusCode, resetData)
	}
	resetOut := decodeMap(t, resetData)
	if resetOut["ok"] != true {
		t.Fatalf("reset response = %s", resetData)
	}
	resetQuota, _ := resetOut["quota"].(map[string]any)
	assertQuotaNumber(t, resetQuota, "used", 0)
	assertQuotaNumber(t, resetQuota, "monthly_limit", 1)
	if resetQuota["exhausted"] != false {
		t.Fatalf("reset quota flags = %v", resetQuota)
	}

	if resp, data := doRequest(t, http.MethodGet, ts.URL+"/search?q=hello&count=2", token, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("search after quota reset: %d %s", resp.StatusCode, data)
	}
	quota = channelQuota(t, channelByName(t, ts, "sx-b"))
	assertQuotaNumber(t, quota, "used", 1)
	if quota["exhausted"] != true {
		t.Fatalf("reused quota after reset = %v", quota)
	}
}
