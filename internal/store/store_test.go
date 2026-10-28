package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"search-gateway/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSeedFromConfig(t *testing.T) {
	st := newTestStore(t)
	cfg := &config.Config{
		Auth: config.Auth{Tokens: []string{"legacy-token"}},
		Search: []config.Provider{
			{Name: "sx", Type: "searxng", BaseURL: "http://127.0.0.1:8888"},
		},
		Fetch: []config.Provider{
			{Name: "direct", Type: "direct"},
		},
	}
	if err := st.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	snapshot := st.Snapshot()
	if len(snapshot.Channels) != 2 || len(snapshot.Routes) != 2 || len(snapshot.APIKeys) != 1 {
		t.Fatalf("seeded: channels=%d routes=%d keys=%d", len(snapshot.Channels), len(snapshot.Routes), len(snapshot.APIKeys))
	}
	key, searchRoute, fetchRoute, err := st.VerifyKey("legacy-token")
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "legacy-1" || searchRoute.Name != "default" || searchRoute.Role != "search" {
		t.Fatalf("key=%+v search=%+v", key, searchRoute)
	}
	if fetchRoute.Name != "default-fetch" || fetchRoute.Role != "fetch" {
		t.Fatalf("fetch route = %+v", fetchRoute)
	}
	if key.RouteID != searchRoute.ID || key.FetchRouteID != fetchRoute.ID {
		t.Fatalf("key routes = %+v", key)
	}
	if len(searchRoute.SearchChannels) != 1 || len(fetchRoute.FetchChannels) != 1 {
		t.Fatalf("route channels = %+v / %+v", searchRoute, fetchRoute)
	}
	// Seeding is idempotent once data exists.
	if err := st.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := st.Snapshot(); len(got.Channels) != 2 {
		t.Fatalf("seed ran twice: %+v", got)
	}
}

func TestChannelValidationAndCRUD(t *testing.T) {
	st := newTestStore(t)

	if _, err := st.CreateChannel(Channel{Name: "t", Type: "tavily", Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing key error = %v", err)
	}
	if _, err := st.CreateChannel(Channel{Name: "x", Type: "nope", Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type error = %v", err)
	}
	if _, err := st.CreateChannel(Channel{Name: "custom", Type: "generic", Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("removed generic type error = %v", err)
	}
	channel, err := st.CreateChannel(Channel{
		Name: "sx", Type: "searxng", Enabled: true,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.ID == "" || channel.Config.Name != "sx" {
		t.Fatalf("channel = %+v", channel)
	}
	if _, err := st.CreateChannel(Channel{Name: "SX", Type: "searxng", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error = %v", err)
	}

	updated, err := st.UpdateChannel(channel.ID, Channel{
		Name: "sx", Type: "searxng", Enabled: false,
		Config: config.Provider{BaseURL: "http://10.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.Config.BaseURL != "http://10.0.0.1:8888" {
		t.Fatalf("update = %+v", updated)
	}

	if _, err := st.CreateRoute(Route{
		Name: "r", Mode: "fallback", Enabled: true, SearchChannels: []string{channel.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChannel(channel.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := st.Snapshot()
	if len(snapshot.Channels) != 0 {
		t.Fatalf("channels left: %+v", snapshot.Channels)
	}
	if len(snapshot.Routes) != 1 || len(snapshot.Routes[0].SearchChannels) != 0 {
		t.Fatalf("route references not cleaned: %+v", snapshot.Routes)
	}
}

func TestRouteValidationAndDeletion(t *testing.T) {
	st := newTestStore(t)
	searchChannel, err := st.CreateChannel(Channel{
		Name: "s", Type: "searxng", Enabled: true,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fetchChannel, err := st.CreateChannel(Channel{Name: "f", Type: "direct", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRoute(Route{Name: "bad", Mode: "fallback", SearchChannels: []string{fetchChannel.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fetch channel as search error = %v", err)
	}
	if _, err := st.CreateRoute(Route{Name: "bad2", Mode: "merge", FetchChannels: []string{searchChannel.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("search channel as fetch error = %v", err)
	}
	searchRoute, err := st.CreateRoute(Route{Name: "ok-search", Role: "search", Mode: "merge", Enabled: true, SearchChannels: []string{searchChannel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	fetchRoute, err := st.CreateRoute(Route{Name: "ok-fetch", Role: "fetch", Mode: "fallback", Enabled: true, FetchChannels: []string{fetchChannel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	key, token, err := st.CreateAPIKey("client", searchRoute.ID, fetchRoute.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRoute(searchRoute.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced route error = %v", err)
	}
	if err := st.DeleteAPIKey(key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.VerifyKey(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deleted key still verifies: %v", err)
	}
	if err := st.DeleteRoute(searchRoute.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRoute(fetchRoute.ID); err != nil {
		t.Fatal(err)
	}
}

func TestKeyLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateChannel(Channel{
		Name: "sx", Type: "searxng", Enabled: true,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := st.CreateRoute(Route{Name: "default", Mode: "fallback", Enabled: true, SearchChannels: []string{channel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	key, token, err := st.CreateAPIKey("client", route.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 20 || key.Hash == "" || key.Hash == token {
		t.Fatalf("key material = %+v token=%q", key, token)
	}
	gotKey, gotSearchRoute, _, err := st.VerifyKey(token)
	if err != nil || gotKey.ID != key.ID || gotSearchRoute.ID != route.ID {
		t.Fatalf("verify = %+v %+v %v", gotKey, gotSearchRoute, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("plaintext token was persisted")
	}

	disabled := false
	if _, err := st.UpdateAPIKey(key.ID, nil, nil, nil, &disabled); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.VerifyKey(token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled key verifies: %v", err)
	}
	enabled := true
	if _, err := st.UpdateAPIKey(key.ID, nil, nil, nil, &enabled); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reopened.VerifyKey(token); err != nil {
		t.Fatalf("persisted key does not verify: %v", err)
	}
	if got := reopened.Snapshot(); got.Revision == 0 || len(got.APIKeys) != 1 {
		t.Fatalf("reopened snapshot = %+v", got)
	}
}

func TestRouteBalanceValidation(t *testing.T) {
	st := newTestStore(t)
	ch, err := st.CreateChannel(Channel{
		Name: "sx", Type: "searxng", Enabled: true,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := st.CreateRoute(Route{Name: "balanced", Mode: "balance", Enabled: true, SearchChannels: []string{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if route.Balance != "round_robin" {
		t.Fatalf("default strategy = %q", route.Balance)
	}
	if _, err := st.CreateRoute(Route{Name: "bad", Mode: "balance", Balance: "weighted", Enabled: true, SearchChannels: []string{ch.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid strategy error = %v", err)
	}
	fallback, err := st.CreateRoute(Route{Name: "fb", Mode: "fallback", Balance: "random", Enabled: true, SearchChannels: []string{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Balance != "" {
		t.Fatalf("balance should be cleared for fallback, got %q", fallback.Balance)
	}
	if _, err := st.CreateRoute(Route{Name: "bad-retries", Mode: "fallback", MaxRetries: -1, Enabled: true, SearchChannels: []string{ch.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative retries error = %v", err)
	}
	retried, err := st.CreateRoute(Route{Name: "retried", Mode: "fallback", MaxRetries: 2, Enabled: true, SearchChannels: []string{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if retried.MaxRetries != 2 {
		t.Fatalf("max retries = %d", retried.MaxRetries)
	}
}

func TestRouteGroupValidation(t *testing.T) {
	st := newTestStore(t)
	chA, err := st.CreateChannel(Channel{Name: "a", Type: "searxng", Enabled: true, Config: config.Provider{BaseURL: "http://127.0.0.1:8888"}})
	if err != nil {
		t.Fatal(err)
	}
	chB, err := st.CreateChannel(Channel{Name: "b", Type: "searxng", Enabled: true, Config: config.Provider{BaseURL: "http://127.0.0.1:8888"}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := st.CreateRoute(Route{Name: "child", Mode: "fallback", Enabled: true, SearchChannels: []string{chA.ID}})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := st.CreateRoute(Route{Name: "parent", Mode: "balance", Enabled: true, SearchGroups: []string{child.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.SearchGroups) != 1 || parent.SearchGroups[0] != child.ID {
		t.Fatalf("parent groups = %+v", parent)
	}

	// A route cannot reference itself.
	selfPatch := parent
	selfPatch.SearchGroups = []string{parent.ID}
	if _, err := st.UpdateRoute(parent.ID, selfPatch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self group error = %v", err)
	}

	// A child cannot reference its parent (cycle).
	cyclePatch := child
	cyclePatch.SearchGroups = []string{parent.ID}
	if _, err := st.UpdateRoute(child.ID, cyclePatch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error = %v", err)
	}

	// A referenced group cannot be deleted.
	if err := st.DeleteRoute(child.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced group error = %v", err)
	}

	// Role capability is checked for groups.
	fetchOnly, err := st.CreateChannel(Channel{Name: "direct-fetch", Type: "direct", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fetchChild, err := st.CreateRoute(Route{Name: "fetch-child", Mode: "fallback", Enabled: true, FetchChannels: []string{fetchOnly.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRoute(Route{Name: "bad-search-group", Mode: "fallback", Enabled: true, SearchGroups: []string{fetchChild.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("role mismatch error = %v", err)
	}
	if _, err := st.CreateRoute(Route{Name: "good-fetch-group", Mode: "fallback", Enabled: true, FetchGroups: []string{fetchChild.ID}}); err != nil {
		t.Fatalf("fetch group error = %v", err)
	}
	_ = chB
}

func TestMigrateLegacyRoutesAndKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	raw := `{
  "version": 1,
  "revision": 7,
  "channels": [
    {"id": "ch_s", "name": "sx", "type": "searxng", "enabled": true, "config": {"base_url": "http://127.0.0.1:8888"}},
    {"id": "ch_f", "name": "direct", "type": "direct", "enabled": true}
  ],
  "routes": [
    {"id": "rt_old", "name": "default", "mode": "fallback", "enabled": true,
     "search_channels": ["ch_s"], "fetch_channels": ["ch_f"]}
  ],
  "api_keys": [
    {"id": "key_old", "name": "legacy-1", "prefix": "legacy-token", "hash": "` + HashToken("legacy-token") + `", "route_id": "rt_old", "enabled": true}
  ]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := st.Snapshot()
	if len(snapshot.Routes) != 2 {
		t.Fatalf("routes after migration = %+v", snapshot.Routes)
	}
	var searchRoute, fetchRoute Route
	for _, route := range snapshot.Routes {
		switch route.Role {
		case "search":
			searchRoute = route
		case "fetch":
			fetchRoute = route
		}
	}
	if searchRoute.ID != "rt_old" || searchRoute.Name != "default" || len(searchRoute.SearchChannels) != 1 {
		t.Fatalf("search route = %+v", searchRoute)
	}
	if fetchRoute.ID == "" || fetchRoute.Role != "fetch" || len(fetchRoute.FetchChannels) != 1 {
		t.Fatalf("fetch route = %+v", fetchRoute)
	}
	key, gotSearch, gotFetch, err := st.VerifyKey("legacy-token")
	if err != nil {
		t.Fatal(err)
	}
	if key.RouteID != searchRoute.ID || key.FetchRouteID != fetchRoute.ID {
		t.Fatalf("migrated key = %+v", key)
	}
	if gotSearch.ID != searchRoute.ID || gotFetch.ID != fetchRoute.ID {
		t.Fatalf("verify routes = %+v / %+v", gotSearch, gotFetch)
	}
}

func TestMigrateRemovesGenericChannels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	raw := `{
  "version": 1,
  "revision": 7,
  "channels": [
    {"id": "ch_s", "name": "sx", "type": "searxng", "enabled": true, "config": {"base_url": "http://127.0.0.1:8888"}},
    {"id": "ch_g", "name": "custom", "type": "generic", "role": "search", "enabled": true, "config": {"url": "https://api.example/q={{query}}"}}
  ],
  "routes": [
    {"id": "rt_search", "name": "default", "role": "search", "mode": "fallback", "enabled": true,
     "search_channels": ["ch_g", "ch_s"], "search_order": ["ch_g", "ch_s"]}
  ],
  "api_keys": []
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := st.Snapshot()
	if len(snapshot.Channels) != 1 || snapshot.Channels[0].ID != "ch_s" {
		t.Fatalf("channels after migration = %+v", snapshot.Channels)
	}
	route := snapshot.Routes[0]
	if len(route.SearchChannels) != 1 || route.SearchChannels[0] != "ch_s" {
		t.Fatalf("route channels after migration = %+v", route.SearchChannels)
	}
	if len(route.SearchOrder) != 1 || route.SearchOrder[0] != "ch_s" {
		t.Fatalf("route order after migration = %+v", route.SearchOrder)
	}
	if snapshot.Revision <= 7 {
		t.Fatalf("revision after migration = %d", snapshot.Revision)
	}
}

func TestChannelMonthlyQuota(t *testing.T) {
	st := newTestStore(t)

	if _, err := st.CreateChannel(Channel{
		Name: "neg", Type: "searxng", Enabled: true, MonthlyQuota: -1,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative quota error = %v", err)
	}

	channel, err := st.CreateChannel(Channel{
		Name: "quota", Type: "searxng", Enabled: true, MonthlyQuota: 100,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.MonthlyQuota != 100 {
		t.Fatalf("created quota = %d", channel.MonthlyQuota)
	}

	updated, err := st.UpdateChannel(channel.ID, Channel{
		Name: "quota", Type: "searxng", Enabled: true, MonthlyQuota: 250,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.MonthlyQuota != 250 {
		t.Fatalf("updated quota = %d", updated.MonthlyQuota)
	}

	// Quota survives a restart; an empty patch value means unlimited.
	reopened, err := Open(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	var stored int64 = -1
	for _, ch := range reopened.Snapshot().Channels {
		if ch.ID == channel.ID {
			stored = ch.MonthlyQuota
		}
	}
	if stored != 250 {
		t.Fatalf("persisted quota = %d", stored)
	}
	if _, err := reopened.UpdateChannel(channel.ID, Channel{
		Name: "quota", Type: "searxng", Enabled: true, MonthlyQuota: 0,
		Config: config.Provider{BaseURL: "http://127.0.0.1:8888"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, ch := range reopened.Snapshot().Channels {
		if ch.ID == channel.ID && ch.MonthlyQuota != 0 {
			t.Fatalf("quota after clear = %d", ch.MonthlyQuota)
		}
	}
}
