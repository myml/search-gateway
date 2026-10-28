package routing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/routing"
	"search-gateway/internal/search"
	"search-gateway/internal/store"
)

// fakeSearxng is a minimal SearXNG-compatible endpoint with a call counter so
// tests can assert that an exhausted channel is never called upstream.
type fakeSearxng struct {
	*httptest.Server
	hits   atomic.Int64
	status int
}

func newFakeSearxng(t *testing.T, tag string) *fakeSearxng {
	t.Helper()
	return newFakeSearxngStatus(t, tag, http.StatusOK)
}

func newFakeSearxngStatus(t *testing.T, tag string, status int) *fakeSearxng {
	t.Helper()
	fake := &fakeSearxng{status: status}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		if fake.status != http.StatusOK {
			http.Error(w, "upstream failure", fake.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"url": "https://example.com/" + tag, "title": tag, "content": "snippet " + tag},
				{"url": "https://example.com/shared", "title": "shared"},
			},
		})
	}))
	t.Cleanup(fake.Server.Close)
	return fake
}

func (f *fakeSearxng) count() int64 { return f.hits.Load() }

// fakePage serves a tiny HTML document and counts fetches.
type fakePage struct {
	*httptest.Server
	hits atomic.Int64
}

func newFakePage(t *testing.T) *fakePage {
	t.Helper()
	page := &fakePage{}
	page.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page.hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Test Page</title></head><body><p>Hello world paragraph.</p></body></html>`)
	}))
	t.Cleanup(page.Server.Close)
	return page
}

// testEnv wires a store, database and routing engine the same way the server
// does, with channels seeded from a temporary configuration.
type testEnv struct {
	cfg         *config.Config
	store       *store.Store
	db          *db.DB
	engine      *routing.Engine
	searchRoute store.Route
	fetchRoute  store.Route
	channels    map[string]store.Channel
}

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

func testConfigJSON(t *testing.T, searchURLs []string, withFetch bool) string {
	t.Helper()
	providers := make([]string, 0, len(searchURLs))
	for i, rawURL := range searchURLs {
		providers = append(providers, fmt.Sprintf(
			`{"name":"sx-%s","type":"searxng","base_url":%q}`, string(rune('a'+i)), rawURL))
	}
	fetchProviders := "[]"
	if withFetch {
		fetchProviders = `[{"name":"direct","type":"direct"}]`
	}
	return fmt.Sprintf(`{
		"listen": "127.0.0.1:0",
		"quota_timezone": "UTC",
		"timeout_seconds": 5,
		"fetch_timeout_seconds": 5,
		"auth": {"tokens": ["legacy-token"]},
		"search": [%s],
		"fetch": %s
	}`, strings.Join(providers, ","), fetchProviders)
}

func newTestEnv(t *testing.T, rawConfig string) *testEnv {
	t.Helper()
	cfg := loadTestConfig(t, rawConfig)
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
	t.Cleanup(func() { _ = database.Close() })

	env := &testEnv{
		cfg:      cfg,
		store:    st,
		db:       database,
		engine:   routing.New(st, cfg, database, logger),
		channels: map[string]store.Channel{},
	}
	snapshot := st.Snapshot()
	for _, ch := range snapshot.Channels {
		env.channels[ch.Name] = ch
	}
	for _, route := range snapshot.Routes {
		switch route.Role {
		case "search":
			if env.searchRoute.ID == "" {
				env.searchRoute = route
			}
		case "fetch":
			if env.fetchRoute.ID == "" {
				env.fetchRoute = route
			}
		}
	}
	if env.searchRoute.ID == "" {
		t.Fatal("seeded search route missing")
	}
	return env
}

// setQuota updates a seeded channel's monthly limit and refreshes the cache.
func (env *testEnv) setQuota(t *testing.T, name string, limit int64) store.Channel {
	t.Helper()
	ch, ok := env.channels[name]
	if !ok {
		t.Fatalf("channel %q not seeded", name)
	}
	ch.MonthlyQuota = limit
	updated, err := env.store.UpdateChannel(ch.ID, ch)
	if err != nil {
		t.Fatalf("set %s quota: %v", name, err)
	}
	env.channels[name] = updated
	return updated
}

func (env *testEnv) createRoute(t *testing.T, route store.Route) store.Route {
	t.Helper()
	route.Enabled = true
	created, err := env.store.CreateRoute(route)
	if err != nil {
		t.Fatalf("create route %q: %v", route.Name, err)
	}
	return created
}

func (env *testEnv) currentPeriod() string {
	return env.cfg.QuotaPeriod(time.Now())
}

func (env *testEnv) usage(t *testing.T) map[string]db.ChannelUsage {
	t.Helper()
	usage, err := env.db.ChannelUsage(env.currentPeriod())
	if err != nil {
		t.Fatalf("channel usage: %v", err)
	}
	return usage
}

func (env *testEnv) channelIDs(names ...string) []string {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		ids = append(ids, env.channels[name].ID)
	}
	return ids
}

func firstTitle(t *testing.T, results []search.Result) string {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("no search results")
	}
	return results[0].Title
}

func TestFallbackSkipsExhaustedChannel(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	sxB := newFakeSearxng(t, "b")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL, sxB.URL}, false))
	env.setQuota(t, "sx-a", 1)

	req := search.SearchRequest{Query: "hello", Count: 2}

	results, meta, err := env.engine.Search(context.Background(), env.searchRoute, req)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if got := firstTitle(t, results); got != "a" {
		t.Fatalf("first search title = %q", got)
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "sx-a" {
		t.Fatalf("first search channels = %v", meta.Channels)
	}

	results, meta, err = env.engine.Search(context.Background(), env.searchRoute, req)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if got := firstTitle(t, results); got != "b" {
		t.Fatalf("second search title = %q, want fallback to sx-b", got)
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "sx-b" {
		t.Fatalf("second search channels = %v", meta.Channels)
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("exhausted channel upstream calls = %d, want 1", got)
	}
	if got := sxB.count(); got != 1 {
		t.Fatalf("fallback channel upstream calls = %d, want 1", got)
	}

	// A channel without a limit keeps serving and keeps counting.
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("third search: %v", err)
	}
	usage := env.usage(t)
	if got := usage[env.channels["sx-a"].ID].Requests; got != 1 {
		t.Fatalf("sx-a used = %d, want 1", got)
	}
	if got := usage[env.channels["sx-b"].ID].Requests; got != 2 {
		t.Fatalf("sx-b used = %d, want 2 (unlimited still counts)", got)
	}
}

func TestAllChannelsExhaustedReturnsQuotaError(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	sxB := newFakeSearxng(t, "b")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL, sxB.URL}, false))
	env.setQuota(t, "sx-a", 1)
	env.setQuota(t, "sx-b", 1)

	req := search.SearchRequest{Query: "hello", Count: 2}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("second search: %v", err)
	}

	results, _, err := env.engine.Search(context.Background(), env.searchRoute, req)
	if !errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("third search error = %v, want ErrQuotaExhausted", err)
	}
	if results != nil {
		t.Fatalf("third search results = %v, want nil", results)
	}
	if !strings.Contains(err.Error(), "monthly quota exhausted") {
		t.Fatalf("third search error %q does not mention monthly quota", err)
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("sx-a upstream calls = %d, want 1", got)
	}
	if got := sxB.count(); got != 1 {
		t.Fatalf("sx-b upstream calls = %d, want 1", got)
	}

	// Skipped attempts must not be recorded in channel_stats either.
	stats := env.engine.Stats()
	if got := stats[env.channels["sx-a"].ID].SearchRequests; got != 1 {
		t.Fatalf("sx-a stats requests = %d, want 1", got)
	}
	if got := stats[env.channels["sx-b"].ID].SearchRequests; got != 1 {
		t.Fatalf("sx-b stats requests = %d, want 1", got)
	}
	if got := stats[env.channels["sx-a"].ID].SearchErrors; got != 0 {
		t.Fatalf("sx-a stats errors = %d, want 0 (quota skip is not an attempt)", got)
	}
}

func TestMergeSkipsExhaustedChannel(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	sxB := newFakeSearxng(t, "b")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL, sxB.URL}, false))
	env.setQuota(t, "sx-a", 1)

	route := env.createRoute(t, store.Route{
		Name: "merged", Role: "search", Mode: "merge",
		SearchChannels: env.channelIDs("sx-a", "sx-b"),
	})
	req := search.SearchRequest{Query: "hello", Count: 10}

	results, meta, err := env.engine.Search(context.Background(), route, req)
	if err != nil {
		t.Fatalf("first merge search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("first merge results = %d, want 3 (shared URL deduped)", len(results))
	}
	if len(meta.Channels) != 2 {
		t.Fatalf("first merge channels = %v", meta.Channels)
	}

	results, meta, err = env.engine.Search(context.Background(), route, req)
	if err != nil {
		t.Fatalf("second merge search should still succeed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("second merge results = %d, want 2 from the remaining channel", len(results))
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "sx-b" {
		t.Fatalf("second merge channels = %v, want [sx-b]", meta.Channels)
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("merged exhausted channel calls = %d, want 1", got)
	}

	// Lowering sx-b below its usage exhausts it too; merge reports the sentinel.
	env.setQuota(t, "sx-b", 1)
	if _, _, err := env.engine.Search(context.Background(), route, req); !errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("all-exhausted merge error = %v, want ErrQuotaExhausted", err)
	}
}

func TestFetchRespectsQuota(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	page := newFakePage(t)
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL}, true))
	if env.fetchRoute.ID == "" {
		t.Fatal("seeded fetch route missing")
	}
	env.setQuota(t, "direct", 1)

	result, meta, err := env.engine.Fetch(context.Background(), env.fetchRoute, page.URL)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if result == nil || result.Title != "Test Page" {
		t.Fatalf("first fetch result = %+v", result)
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "direct" {
		t.Fatalf("first fetch channels = %v", meta.Channels)
	}

	if _, _, err := env.engine.Fetch(context.Background(), env.fetchRoute, page.URL); !errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("second fetch error = %v, want ErrQuotaExhausted", err)
	}
	if got := page.hits.Load(); got != 1 {
		t.Fatalf("page fetches = %d, want 1 (exhausted channel must not be called)", got)
	}
	usage := env.usage(t)
	if got := usage[env.channels["direct"].ID].Requests; got != 1 {
		t.Fatalf("direct used = %d, want 1", got)
	}
}

func TestFailedCallCountsQuotaUsageAndError(t *testing.T) {
	sxBad := newFakeSearxngStatus(t, "bad", http.StatusInternalServerError)
	env := newTestEnv(t, testConfigJSON(t, []string{sxBad.URL}, false))
	env.setQuota(t, "sx-a", 5)
	channelID := env.channels["sx-a"].ID

	req := search.SearchRequest{Query: "hello", Count: 2}
	_, _, err := env.engine.Search(context.Background(), env.searchRoute, req)
	if err == nil {
		t.Fatal("expected upstream failure")
	}
	if errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("upstream failure was misreported as quota exhaustion: %v", err)
	}
	usage := env.usage(t)
	if got := usage[channelID]; got.Requests != 1 || got.Errors != 1 {
		t.Fatalf("usage after failure = %+v, want requests=1 errors=1", got)
	}
	stats := env.engine.Stats()
	if got := stats[channelID]; got.SearchRequests != 1 || got.SearchErrors != 1 {
		t.Fatalf("channel stats after failure = %+v, want requests=1 errors=1", got)
	}

	// A failed call still consumes the quota.
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err == nil {
		t.Fatal("expected second upstream failure")
	}
	usage = env.usage(t)
	if got := usage[channelID]; got.Requests != 2 || got.Errors != 2 {
		t.Fatalf("usage after second failure = %+v, want requests=2 errors=2", got)
	}
	if got := sxBad.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestQuotaPeriodRollover(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL}, false))
	ch := env.setQuota(t, "sx-a", 1)
	currentPeriod := env.currentPeriod()
	previousPeriod := env.cfg.QuotaPeriod(time.Now().AddDate(0, -1, 0))
	if previousPeriod == currentPeriod {
		t.Fatalf("previous period %q equals current period", previousPeriod)
	}

	// Usage from the previous month must not block the current month.
	if allowed, err := env.db.ConsumeChannelQuota(ch.ID, previousPeriod, 1); err != nil || !allowed {
		t.Fatalf("seed previous period usage: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := env.db.ConsumeChannelQuota(ch.ID, previousPeriod, 1); err != nil || allowed {
		t.Fatalf("previous period should be exhausted: allowed=%v err=%v", allowed, err)
	}

	req := search.SearchRequest{Query: "hello", Count: 2}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("search in new period: %v", err)
	}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); !errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("second search error = %v, want ErrQuotaExhausted", err)
	}

	// The next natural month starts with an empty counter; resetting the
	// current period is the equivalent state transition.
	if err := env.db.ResetChannelUsage(currentPeriod, ch.ID); err != nil {
		t.Fatalf("reset current period: %v", err)
	}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("search after period rollover: %v", err)
	}
	if got := sxA.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
	usage := env.usage(t)
	if got := usage[ch.ID].Requests; got != 1 {
		t.Fatalf("current period usage after reset = %d, want 1", got)
	}
}

func TestNestedGroupSkipsExhaustedGroup(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	sxB := newFakeSearxng(t, "b")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL, sxB.URL}, false))
	env.setQuota(t, "sx-a", 1)

	groupA := env.createRoute(t, store.Route{
		Name: "group-a", Role: "search", Mode: "fallback",
		SearchChannels: env.channelIDs("sx-a"),
	})
	groupB := env.createRoute(t, store.Route{
		Name: "group-b", Role: "search", Mode: "fallback",
		SearchChannels: env.channelIDs("sx-b"),
	})
	parent := env.createRoute(t, store.Route{
		Name: "nested", Role: "search", Mode: "fallback",
		SearchGroups: []string{groupA.ID, groupB.ID},
	})

	req := search.SearchRequest{Query: "hello", Count: 2}
	results, meta, err := env.engine.Search(context.Background(), parent, req)
	if err != nil {
		t.Fatalf("first nested search: %v", err)
	}
	if got := firstTitle(t, results); got != "a" {
		t.Fatalf("first nested title = %q", got)
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "sx-a" {
		t.Fatalf("first nested channels = %v", meta.Channels)
	}

	results, meta, err = env.engine.Search(context.Background(), parent, req)
	if err != nil {
		t.Fatalf("second nested search: %v", err)
	}
	if got := firstTitle(t, results); got != "b" {
		t.Fatalf("second nested title = %q, want group fallback to sx-b", got)
	}
	if len(meta.Channels) != 1 || meta.Channels[0] != "sx-b" {
		t.Fatalf("second nested channels = %v", meta.Channels)
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("exhausted nested channel calls = %d, want 1", got)
	}
}

func TestBalanceSkipsExhaustedChannel(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	sxB := newFakeSearxng(t, "b")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL, sxB.URL}, false))
	env.setQuota(t, "sx-a", 1)

	route := env.createRoute(t, store.Route{
		Name: "balanced", Role: "search", Mode: "balance", Balance: "round_robin",
		SearchChannels: env.channelIDs("sx-a", "sx-b"),
	})
	req := search.SearchRequest{Query: "hello", Count: 2}

	wantTitles := []string{"a", "b", "b", "b"}
	for i, want := range wantTitles {
		results, _, err := env.engine.Search(context.Background(), route, req)
		if err != nil {
			t.Fatalf("balance search %d: %v", i, err)
		}
		if got := firstTitle(t, results); got != want {
			t.Fatalf("balance search %d title = %q, want %q", i, got, want)
		}
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("exhausted balance channel calls = %d, want 1", got)
	}
	if got := sxB.count(); got != 3 {
		t.Fatalf("healthy balance channel calls = %d, want 3", got)
	}
}

func TestQuotaCounterFailureFailsOpen(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL}, false))
	env.setQuota(t, "sx-a", 1)
	if err := env.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	results, _, err := env.engine.Search(context.Background(), env.searchRoute,
		search.SearchRequest{Query: "hello", Count: 2})
	if err != nil {
		t.Fatalf("search with closed counter db should fail open: %v", err)
	}
	if got := firstTitle(t, results); got != "a" {
		t.Fatalf("fail-open title = %q", got)
	}
	if got := sxA.count(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestTestChannelBypassesQuota(t *testing.T) {
	sxA := newFakeSearxng(t, "a")
	env := newTestEnv(t, testConfigJSON(t, []string{sxA.URL}, false))
	ch := env.setQuota(t, "sx-a", 1)

	req := search.SearchRequest{Query: "hello", Count: 2}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); err != nil {
		t.Fatalf("first search: %v", err)
	}
	if _, _, err := env.engine.Search(context.Background(), env.searchRoute, req); !errors.Is(err, routing.ErrQuotaExhausted) {
		t.Fatalf("second search error = %v, want ErrQuotaExhausted", err)
	}

	// The admin connectivity test must neither be blocked nor counted.
	result, err := env.engine.TestChannel(context.Background(), ch, "hello", "")
	if err != nil {
		t.Fatalf("TestChannel at quota: %v", err)
	}
	if result == nil {
		t.Fatal("TestChannel returned no result")
	}
	if got := sxA.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
	usage := env.usage(t)
	if got := usage[ch.ID].Requests; got != 1 {
		t.Fatalf("usage after admin test = %d, want 1", got)
	}
}
