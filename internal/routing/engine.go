// Package routing executes a route over its ordered channels, with fallback and
// merge aggregation, and caches per-channel provider instances.
package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/search"
	"search-gateway/internal/store"
)

// ErrNoChannel means the route has no usable channel for the requested role.
var ErrNoChannel = errors.New("route has no usable channel")

// ErrQuotaExhausted means a channel was skipped because it reached its monthly
// call quota. When every candidate of a route is in that state, the route
// returns an error wrapping this sentinel.
var ErrQuotaExhausted = errors.New("channel monthly quota exhausted")

// Meta describes how a request was served.
type Meta struct {
	RouteID   string
	RouteName string
	Mode      string
	Channels  []string
}

// ChannelStats is the runtime usage counter of one channel.
type ChannelStats struct {
	SearchRequests int64      `json:"search_requests"`
	SearchErrors   int64      `json:"search_errors"`
	FetchRequests  int64      `json:"fetch_requests"`
	FetchErrors    int64      `json:"fetch_errors"`
	LastError      string     `json:"last_error,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

type channelRuntime struct {
	updatedAt time.Time
	search    search.Provider
	searchErr error
	fetch     search.Fetcher
	fetchErr  error
}

// Engine resolves channels from the store and executes searches/fetches.
type Engine struct {
	store    *store.Store
	cfg      *config.Config
	log      *slog.Logger
	mu       sync.Mutex
	runtimes map[string]*channelRuntime
	db       *db.DB
	rrMu     sync.Mutex
	rr       map[string]uint64
}

// New builds an engine.
func New(st *store.Store, cfg *config.Config, database *db.DB, log *slog.Logger) *Engine {
	return &Engine{
		store:    st,
		cfg:      cfg,
		log:      log,
		runtimes: map[string]*channelRuntime{},
		db:       database,
		rr:       map[string]uint64{},
	}
}

const maxRouteDepth = 8

// routeTarget is either a directly configured channel or a nested route/group.
type routeTarget struct {
	channel *store.Channel
	route   *store.Route
}

func (t routeTarget) name() string {
	if t.channel != nil {
		return t.channel.Name
	}
	if t.route != nil {
		return t.route.Name
	}
	return ""
}

// Search runs a search through the route. Routes may reference nested routes
// (groups), so each group executes with its own mode.
func (e *Engine) Search(ctx context.Context, route store.Route, req search.SearchRequest) ([]search.Result, Meta, error) {
	meta := Meta{RouteID: route.ID, RouteName: route.Name, Mode: route.Mode}
	if route.Role != "" && route.Role != "search" {
		return nil, meta, fmt.Errorf("route %q is not a search route", route.Name)
	}
	if meta.Mode == "" {
		meta.Mode = "fallback"
	}
	results, channels, err := e.searchRoute(ctx, route, req, 0, map[string]bool{})
	meta.Channels = channels
	return results, meta, err
}

func (e *Engine) searchRoute(ctx context.Context, route store.Route, req search.SearchRequest, depth int, visited map[string]bool) ([]search.Result, []string, error) {
	if depth > maxRouteDepth {
		return nil, nil, fmt.Errorf("route nesting exceeds %d levels", maxRouteDepth)
	}
	if visited[route.ID] {
		return nil, nil, fmt.Errorf("route group cycle at %q", route.Name)
	}
	visited[route.ID] = true
	defer delete(visited, route.ID)

	targets := e.searchTargets(route)
	if len(targets) == 0 {
		return nil, nil, ErrNoChannel
	}
	switch route.Mode {
	case "merge":
		return e.mergeSearchTargets(ctx, targets, req, depth, visited)
	case "balance":
		targets = e.balanceTargets(route, "search", targets)
	}
	return e.fallbackSearchTargets(ctx, targets, req, route.FallbackOnEmpty, route.MaxRetries, depth, visited)
}

func (e *Engine) fallbackSearchTargets(ctx context.Context, targets []routeTarget, req search.SearchRequest, fallbackOnEmpty bool, maxRetries, depth int, visited map[string]bool) ([]search.Result, []string, error) {
	var errs []error
	var lastEmpty []search.Result
	var lastChannels []string
	limit := attemptLimit(len(targets), maxRetries)
	for i := 0; i < limit; i++ {
		target := targets[i]
		if target.channel != nil {
			ch := *target.channel
			rt := e.runtimeFor(ch)
			if rt.searchErr != nil {
				e.recordError(ch.ID, "search", rt.searchErr)
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, rt.searchErr))
				continue
			}
			if !e.consumeQuota(ch) {
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, ErrQuotaExhausted))
				continue
			}
			e.recordAttempt(ch.ID, "search")
			results, err := rt.search.Search(ctx, req)
			if err != nil {
				e.recordError(ch.ID, "search", err)
				e.recordUsageError(ch)
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, err))
				e.log.Warn("channel search failed", "channel", ch.Name, "query", req.Query, "err", err)
				continue
			}
			channels := []string{ch.Name}
			if len(results) == 0 {
				lastEmpty, lastChannels = results, channels
				if fallbackOnEmpty && i < limit-1 {
					e.log.Info("channel returned no results, falling back", "channel", ch.Name)
					continue
				}
				return results, channels, nil
			}
			return results, channels, nil
		}

		child := *target.route
		results, channels, err := e.searchRoute(ctx, child, req, depth+1, visited)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", child.Name, err))
			continue
		}
		if len(results) == 0 {
			lastEmpty, lastChannels = results, channels
			if fallbackOnEmpty && i < limit-1 {
				e.log.Info("group returned no results, falling back", "group", child.Name)
				continue
			}
			return results, channels, nil
		}
		return results, channels, nil
	}
	if lastEmpty != nil {
		return lastEmpty, lastChannels, nil
	}
	return nil, nil, fmt.Errorf("all search channels failed: %w", errors.Join(errs...))
}

type mergeTargetOutcome struct {
	channels []string
	results  []search.Result
	err      error
}

func (e *Engine) mergeSearchTargets(ctx context.Context, targets []routeTarget, req search.SearchRequest, depth int, visited map[string]bool) ([]search.Result, []string, error) {
	outcomes := make([]mergeTargetOutcome, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target routeTarget) {
			defer wg.Done()
			if target.channel != nil {
				ch := *target.channel
				rt := e.runtimeFor(ch)
				if rt.searchErr != nil {
					e.recordError(ch.ID, "search", rt.searchErr)
					outcomes[i].err = fmt.Errorf("%s: %w", ch.Name, rt.searchErr)
					return
				}
				if !e.consumeQuota(ch) {
					outcomes[i].err = fmt.Errorf("%s: %w", ch.Name, ErrQuotaExhausted)
					return
				}
				e.recordAttempt(ch.ID, "search")
				results, err := rt.search.Search(ctx, req)
				if err != nil {
					e.recordError(ch.ID, "search", err)
					e.recordUsageError(ch)
					outcomes[i].err = fmt.Errorf("%s: %w", ch.Name, err)
					return
				}
				outcomes[i].channels = []string{ch.Name}
				outcomes[i].results = results
				return
			}
			child := *target.route
			results, channels, err := e.searchRoute(ctx, child, req, depth+1, cloneVisited(visited))
			outcomes[i].channels = channels
			outcomes[i].results = results
			if err != nil {
				outcomes[i].err = fmt.Errorf("%s: %w", child.Name, err)
			}
		}(i, target)
	}
	wg.Wait()

	var merged []search.Result
	var channels []string
	var errs []error
	seen := map[string]struct{}{}
	succeeded := 0
	for _, out := range outcomes {
		if out.err != nil {
			errs = append(errs, out.err)
			continue
		}
		succeeded++
		channels = append(channels, out.channels...)
		for _, r := range out.results {
			if r.URL == "" {
				continue
			}
			if _, ok := seen[r.URL]; ok {
				continue
			}
			seen[r.URL] = struct{}{}
			merged = append(merged, r)
		}
	}
	if succeeded == 0 {
		return nil, nil, fmt.Errorf("all merge channels failed: %w", errors.Join(errs...))
	}
	limit := req.Count
	if limit <= 0 {
		limit = 8
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, channels, nil
}

// Fetch retrieves one URL through the route, optionally across nested groups.
func (e *Engine) Fetch(ctx context.Context, route store.Route, rawURL string) (*search.FetchResult, Meta, error) {
	meta := Meta{RouteID: route.ID, RouteName: route.Name, Mode: route.Mode}
	if route.Role != "" && route.Role != "fetch" {
		return nil, meta, fmt.Errorf("route %q is not a fetch route", route.Name)
	}
	if meta.Mode == "" {
		meta.Mode = "fallback"
	}
	result, channels, err := e.fetchRoute(ctx, route, rawURL, 0, map[string]bool{})
	meta.Channels = channels
	return result, meta, err
}

func (e *Engine) fetchRoute(ctx context.Context, route store.Route, rawURL string, depth int, visited map[string]bool) (*search.FetchResult, []string, error) {
	if depth > maxRouteDepth {
		return nil, nil, fmt.Errorf("route nesting exceeds %d levels", maxRouteDepth)
	}
	if visited[route.ID] {
		return nil, nil, fmt.Errorf("route group cycle at %q", route.Name)
	}
	visited[route.ID] = true
	defer delete(visited, route.ID)

	targets := e.fetchTargets(route)
	if len(targets) == 0 {
		return nil, nil, ErrNoChannel
	}
	if route.Mode == "balance" {
		targets = e.balanceTargets(route, "fetch", targets)
	}
	var errs []error
	limit := attemptLimit(len(targets), route.MaxRetries)
	for i := 0; i < limit; i++ {
		target := targets[i]
		if target.channel != nil {
			ch := *target.channel
			rt := e.runtimeFor(ch)
			if rt.fetchErr != nil {
				e.recordError(ch.ID, "fetch", rt.fetchErr)
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, rt.fetchErr))
				continue
			}
			if !e.consumeQuota(ch) {
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, ErrQuotaExhausted))
				continue
			}
			e.recordAttempt(ch.ID, "fetch")
			result, err := rt.fetch.Fetch(ctx, rawURL)
			if err != nil {
				e.recordError(ch.ID, "fetch", err)
				e.recordUsageError(ch)
				errs = append(errs, fmt.Errorf("%s: %w", ch.Name, err))
				e.log.Warn("channel fetch failed", "channel", ch.Name, "url", rawURL, "err", err)
				continue
			}
			return result, []string{ch.Name}, nil
		}
		child := *target.route
		result, channels, err := e.fetchRoute(ctx, child, rawURL, depth+1, visited)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", child.Name, err))
			continue
		}
		return result, channels, nil
	}
	return nil, nil, fmt.Errorf("all fetch channels failed: %w", errors.Join(errs...))
}

func (e *Engine) searchTargets(route store.Route) []routeTarget {
	snapshot := e.store.Snapshot()
	channels := make(map[string]store.Channel, len(snapshot.Channels))
	for _, ch := range snapshot.Channels {
		channels[ch.ID] = ch
	}
	routes := make(map[string]store.Route, len(snapshot.Routes))
	for _, rt := range snapshot.Routes {
		routes[rt.ID] = rt
	}
	refs := orderRefs(route.SearchOrder, route.SearchChannels, route.SearchGroups)
	targets := make([]routeTarget, 0, len(refs))
	for _, id := range refs {
		if ch, ok := channels[id]; ok {
			if ch.Enabled && ch.Supports("search") {
				targets = append(targets, routeTarget{channel: &ch})
			}
			continue
		}
		if rt, ok := routes[id]; ok && rt.Enabled && rt.Role == "search" {
			targets = append(targets, routeTarget{route: &rt})
		}
	}
	return targets
}

func (e *Engine) fetchTargets(route store.Route) []routeTarget {
	snapshot := e.store.Snapshot()
	channels := make(map[string]store.Channel, len(snapshot.Channels))
	for _, ch := range snapshot.Channels {
		channels[ch.ID] = ch
	}
	routes := make(map[string]store.Route, len(snapshot.Routes))
	for _, rt := range snapshot.Routes {
		routes[rt.ID] = rt
	}
	refs := orderRefs(route.FetchOrder, route.FetchChannels, route.FetchGroups)
	targets := make([]routeTarget, 0, len(refs))
	for _, id := range refs {
		if ch, ok := channels[id]; ok {
			if ch.Enabled && ch.Supports("fetch") {
				targets = append(targets, routeTarget{channel: &ch})
			}
			continue
		}
		if rt, ok := routes[id]; ok && rt.Enabled && rt.Role == "fetch" {
			targets = append(targets, routeTarget{route: &rt})
		}
	}
	return targets
}

func attemptLimit(total, maxRetries int) int {
	if total < 1 {
		return 0
	}
	if maxRetries <= 0 || maxRetries+1 >= total {
		return total
	}
	return maxRetries + 1
}

func orderRefs(order, channels, groups []string) []string {
	if len(order) > 0 {
		return order
	}
	refs := make([]string, 0, len(channels)+len(groups))
	refs = append(refs, channels...)
	refs = append(refs, groups...)
	return refs
}

func (e *Engine) balanceTargets(route store.Route, role string, targets []routeTarget) []routeTarget {
	if len(targets) < 2 {
		return targets
	}
	start := e.balanceStart(route, role, len(targets))
	ordered := make([]routeTarget, 0, len(targets))
	ordered = append(ordered, targets[start:]...)
	ordered = append(ordered, targets[:start]...)
	return ordered
}

func (e *Engine) balanceStart(route store.Route, role string, count int) int {
	strategy := route.Balance
	if strategy == "" {
		strategy = "round_robin"
	}
	if strategy == "random" {
		return rand.IntN(count)
	}
	e.rrMu.Lock()
	key := route.ID + ":" + role
	n := e.rr[key]
	e.rr[key] = n + 1
	e.rrMu.Unlock()
	return int(n % uint64(count))
}

func cloneVisited(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for id, value := range in {
		out[id] = value
	}
	return out
}

func (e *Engine) runtimeFor(ch store.Channel) *channelRuntime {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rt, ok := e.runtimes[ch.ID]; ok && rt.updatedAt.Equal(ch.UpdatedAt) {
		return rt
	}
	rt := e.buildRuntime(ch)
	e.runtimes[ch.ID] = rt
	return rt
}

func (e *Engine) buildRuntime(ch store.Channel) *channelRuntime {
	rt := &channelRuntime{updatedAt: ch.UpdatedAt}
	pc := ch.Config
	pc.Name = ch.Name
	pc.Type = ch.Type
	proxy := pc.Proxy
	if proxy == "" {
		proxy = e.cfg.Proxy
	}
	insecure := e.cfg.InsecureSkipVerify
	if pc.InsecureSkipVerify != nil {
		insecure = *pc.InsecureSkipVerify
	}
	if ch.Supports("search") {
		client, err := search.NewClient(proxy, insecure, e.cfg.ProviderTimeout(pc, e.cfg.SearchTimeout()))
		if err != nil {
			rt.searchErr = err
		} else {
			provider, buildErr := search.BuildSearchProvider(pc, client, e.cfg.UserAgent)
			if buildErr != nil {
				rt.searchErr = buildErr
			} else {
				rt.search = provider
			}
		}
	}
	if ch.Supports("fetch") {
		client, err := search.NewClient(proxy, insecure, e.cfg.ProviderTimeout(pc, e.cfg.FetchTimeout()))
		if err != nil {
			rt.fetchErr = err
		} else {
			fetcher, buildErr := search.BuildFetchProvider(pc, client, e.cfg.UserAgent)
			if buildErr != nil {
				rt.fetchErr = buildErr
			} else {
				rt.fetch = fetcher
			}
		}
	}
	return rt
}

// ValidateChannel builds the channel providers without caching them, so admin
// writes fail fast on bad credentials or endpoints.
func (e *Engine) ValidateChannel(ch store.Channel) error {
	rt := e.buildRuntime(ch)
	if rt.search != nil || rt.fetch != nil {
		return nil
	}
	if rt.searchErr != nil {
		return rt.searchErr
	}
	if rt.fetchErr != nil {
		return rt.fetchErr
	}
	return fmt.Errorf("unsupported channel type %q", ch.Type)
}

// TestChannel runs one request directly against a channel, bypassing routes.
func (e *Engine) TestChannel(ctx context.Context, ch store.Channel, query, rawURL string) (any, error) {
	rt := e.buildRuntime(ch)
	if query != "" {
		if rt.searchErr != nil {
			return nil, rt.searchErr
		}
		if rt.search == nil {
			return nil, fmt.Errorf("channel %q cannot search", ch.Name)
		}
		results, err := rt.search.Search(ctx, search.SearchRequest{Query: query, Count: e.cfg.DefaultCount})
		if err != nil {
			return nil, err
		}
		return results, nil
	}
	if rawURL != "" {
		if rt.fetchErr != nil {
			return nil, rt.fetchErr
		}
		if rt.fetch == nil {
			return nil, fmt.Errorf("channel %q cannot fetch", ch.Name)
		}
		return rt.fetch.Fetch(ctx, rawURL)
	}
	return nil, fmt.Errorf("query or url is required")
}

// consumeQuota counts one upstream call against the channel's monthly quota
// immediately before the provider is invoked. It reports false when the channel
// has already served its limit, so the caller must skip it. Counter failures
// fail open: a broken database must not take the gateway down.
func (e *Engine) consumeQuota(ch store.Channel) bool {
	period := e.cfg.QuotaPeriod(time.Now())
	allowed, err := e.db.ConsumeChannelQuota(ch.ID, period, ch.MonthlyQuota)
	if err != nil {
		e.log.Warn("consume channel quota failed, allowing request",
			"channel", ch.Name, "period", period, "err", err)
		return true
	}
	return allowed
}

// recordUsageError counts a failed upstream call in the channel's quota period.
// The call itself was already counted by consumeQuota.
func (e *Engine) recordUsageError(ch store.Channel) {
	period := e.cfg.QuotaPeriod(time.Now())
	if err := e.db.RecordChannelUsageError(ch.ID, period); err != nil {
		e.log.Warn("record channel quota error failed",
			"channel", ch.Name, "period", period, "err", err)
	}
}

func (e *Engine) recordAttempt(id, role string) {
	if err := e.db.RecordChannelAttempt(id, role, nil); err != nil {
		e.log.Warn("record channel usage failed", "channel", id, "role", role, "err", err)
	}
}

func (e *Engine) recordError(id, role string, err error) {
	if dbErr := e.db.RecordChannelAttempt(id, role, err); dbErr != nil {
		e.log.Warn("record channel error failed", "channel", id, "role", role, "err", dbErr)
	}
}

// Stats returns channel usage counters from the database.
func (e *Engine) Stats() map[string]ChannelStats {
	stored, err := e.db.ChannelStats()
	if err != nil {
		e.log.Warn("load channel stats failed", "err", err)
		return map[string]ChannelStats{}
	}
	out := make(map[string]ChannelStats, len(stored))
	for id, stat := range stored {
		out[id] = ChannelStats{
			SearchRequests: stat.SearchRequests,
			SearchErrors:   stat.SearchErrors,
			FetchRequests:  stat.FetchRequests,
			FetchErrors:    stat.FetchErrors,
			LastError:      stat.LastError,
			LastUsedAt:     stat.LastUsedAt,
		}
	}
	return out
}
