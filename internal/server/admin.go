package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/logstore"
	"search-gateway/internal/routing"
	"search-gateway/internal/search"
	"search-gateway/internal/store"
)

// quotaSnapshot is the channel usage state of one request. Every DTO builder
// receives it so a channel listing only queries SQLite once, and all channels
// report the same period/reset boundary.
type quotaSnapshot struct {
	period   string
	resetsAt time.Time
	usage    map[string]db.ChannelUsage
}

func (s *Server) loadQuotaSnapshot() quotaSnapshot {
	now := time.Now()
	period := s.cfg.QuotaPeriod(now)
	_, resetsAt := s.cfg.QuotaPeriodBounds(now)
	usage, err := s.db.ChannelUsage(period)
	if err != nil {
		s.log.Warn("load channel usage failed", "period", period, "err", err)
		usage = map[string]db.ChannelUsage{}
	}
	return quotaSnapshot{period: period, resetsAt: resetsAt, usage: usage}
}

// channelQuotaDTO renders the monthly quota object of one channel. A limit of
// zero means unlimited: remaining stays zero and exhausted is always false,
// while usage keeps being reported.
func channelQuotaDTO(ch store.Channel, snapshot quotaSnapshot) map[string]any {
	used := snapshot.usage[ch.ID]
	unlimited := ch.MonthlyQuota <= 0
	remaining := int64(0)
	exhausted := false
	if !unlimited {
		remaining = ch.MonthlyQuota - used.Requests
		if remaining < 0 {
			remaining = 0
		}
		exhausted = used.Requests >= ch.MonthlyQuota
	}
	return map[string]any{
		"monthly_limit": ch.MonthlyQuota,
		"used":          used.Requests,
		"errors":        used.Errors,
		"remaining":     remaining,
		"unlimited":     unlimited,
		"exhausted":     exhausted,
		"period":        snapshot.period,
		"resets_at":     snapshot.resetsAt,
	}
}

func (s *Server) channelDTO(ch store.Channel, stat *routing.ChannelStats, quota quotaSnapshot) map[string]any {
	cfg := map[string]any{
		"base_url":        ch.Config.BaseURL,
		"proxy":           ch.Config.Proxy,
		"timeout_seconds": ch.Config.TimeoutSeconds,
		"headers":         ch.Config.Headers,
		"api_key_set":     ch.Config.APIKey != "",
		"api_key_env":     ch.Config.APIKeyEnv,
		"max_bytes":       ch.Config.MaxBytes,
		"user_agent":      ch.Config.UserAgent,
	}
	var stats any
	if stat != nil {
		stats = stat
	}
	return map[string]any{
		"id":           ch.ID,
		"name":         ch.Name,
		"type":         ch.Type,
		"enabled":      ch.Enabled,
		"capabilities": ch.Capabilities(),
		"config":       cfg,
		"stats":        stats,
		"tests":        s.channelChecks(ch.ID),
		"quota":        channelQuotaDTO(ch, quota),
		"created_at":   ch.CreatedAt,
		"updated_at":   ch.UpdatedAt,
	}
}

func routeDTO(route store.Route) map[string]any {
	return map[string]any{
		"id":                route.ID,
		"name":              route.Name,
		"description":       route.Description,
		"role":              route.Role,
		"mode":              route.Mode,
		"balance":           route.Balance,
		"max_retries":       route.MaxRetries,
		"fallback_on_empty": route.FallbackOnEmpty,
		"search_channels":   route.SearchChannels,
		"fetch_channels":    route.FetchChannels,
		"search_groups":     route.SearchGroups,
		"fetch_groups":      route.FetchGroups,
		"search_order":      route.SearchOrder,
		"fetch_order":       route.FetchOrder,
		"enabled":           route.Enabled,
		"created_at":        route.CreatedAt,
		"updated_at":        route.UpdatedAt,
	}
}

func (s *Server) keyDTO(key store.APIKey, routeNames map[string]string, stats map[string]KeyStats) map[string]any {
	stat := keyStatFor(stats, key)
	return map[string]any{
		"id":                key.ID,
		"name":              key.Name,
		"prefix":            key.Prefix,
		"route_id":          key.RouteID,
		"route_name":        routeNames[key.RouteID],
		"search_route_id":   key.RouteID,
		"search_route_name": routeNames[key.RouteID],
		"fetch_route_id":    key.FetchRouteID,
		"fetch_route_name":  routeNames[key.FetchRouteID],
		"enabled":           key.Enabled,
		"stats":             stat,
		"created_at":        key.CreatedAt,
		"updated_at":        key.UpdatedAt,
	}
}

func (s *Server) channelDTOs(snapshot store.Data) []any {
	stats := s.engine.Stats()
	quota := s.loadQuotaSnapshot()
	out := make([]any, 0, len(snapshot.Channels))
	for _, ch := range snapshot.Channels {
		var stat *routing.ChannelStats
		if v, ok := stats[ch.ID]; ok {
			stat = &v
		}
		out = append(out, s.channelDTO(ch, stat, quota))
	}
	return out
}

func (s *Server) routeDTOs(snapshot store.Data) []any {
	out := make([]any, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		out = append(out, routeDTO(route))
	}
	return out
}

func (s *Server) keyDTOs(snapshot store.Data) []any {
	routeNames := make(map[string]string, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		routeNames[route.ID] = route.Name
	}
	stats := s.keyStatsSnapshot()
	out := make([]any, 0, len(snapshot.APIKeys))
	for _, key := range snapshot.APIKeys {
		out = append(out, s.keyDTO(key, routeNames, stats))
	}
	return out
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    Version,
		"store_path": s.store.Path(),
		"db_path":    s.cfg.DBPath,
		"revision":   snapshot.Revision,
		"updated_at": snapshot.UpdatedAt,
		"counts": map[string]int{
			"channels": len(snapshot.Channels),
			"routes":   len(snapshot.Routes),
			"api_keys": len(snapshot.APIKeys),
		},
		"channels": s.channelDTOs(snapshot),
		"routes":   s.routeDTOs(snapshot),
		"keys":     s.keyDTOs(snapshot),
	})
}

func (s *Server) handleAdminListChannels(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"channels": s.channelDTOs(snapshot)})
}

// channelInput is a partial channel update. A nil field was absent from the
// request body and keeps its current value, so old clients that PATCH only the
// enabled flag (or only the quota) never wipe the other fields.
type channelInput struct {
	Name         *string          `json:"name"`
	Type         *string          `json:"type"`
	Enabled      *bool            `json:"enabled"`
	Config       *config.Provider `json:"config"`
	MonthlyQuota *int64           `json:"monthly_quota"`
}

func (s *Server) handleAdminCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	candidate := store.Channel{Enabled: true}
	if in.Enabled != nil {
		candidate.Enabled = *in.Enabled
	}
	if in.Name != nil {
		candidate.Name = *in.Name
	}
	if in.Type != nil {
		candidate.Type = *in.Type
	}
	if in.Config != nil {
		candidate.Config = *in.Config
	}
	if in.MonthlyQuota != nil {
		candidate.MonthlyQuota = *in.MonthlyQuota
	}
	if err := s.engine.ValidateChannel(candidate); err != nil {
		writeError(w, http.StatusBadRequest, "channel validation failed: "+err.Error())
		return
	}
	created, err := s.store.CreateChannel(candidate)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.channelDTO(created, nil, s.loadQuotaSnapshot()))
}

func (s *Server) handleAdminUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in channelInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	snapshot := s.store.Snapshot()
	var current *store.Channel
	for i := range snapshot.Channels {
		if snapshot.Channels[i].ID == id {
			ch := snapshot.Channels[i]
			current = &ch
			break
		}
	}
	if current == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	effective := *current
	if in.Name != nil {
		effective.Name = *in.Name
	}
	if in.Type != nil {
		effective.Type = *in.Type
	}
	if in.Enabled != nil {
		effective.Enabled = *in.Enabled
	}
	if in.Config != nil {
		effective.Config = *in.Config
		if strings.TrimSpace(effective.Config.APIKey) == "" {
			effective.Config.APIKey = current.Config.APIKey
		} else if effective.Config.APIKey == "__clear__" {
			effective.Config.APIKey = ""
		}
	}
	if in.MonthlyQuota != nil {
		effective.MonthlyQuota = *in.MonthlyQuota
	}
	if err := s.engine.ValidateChannel(effective); err != nil {
		writeError(w, http.StatusBadRequest, "channel validation failed: "+err.Error())
		return
	}
	// UpdateChannel replaces the whole config; keep the "__clear__" marker so it
	// can still remove a stored API key.
	patchConfig := effective.Config
	if in.Config != nil && in.Config.APIKey == "__clear__" {
		patchConfig.APIKey = "__clear__"
	}
	updated, err := s.store.UpdateChannel(id, store.Channel{
		Name: effective.Name, Type: effective.Type, Config: patchConfig,
		Enabled: effective.Enabled, MonthlyQuota: effective.MonthlyQuota,
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.channelDTO(updated, nil, s.loadQuotaSnapshot()))
}

func (s *Server) handleAdminDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteChannel(id); err != nil {
		s.storeError(w, err)
		return
	}
	// Quota rows live in SQLite and are not part of the JSON store, so clean
	// them up best-effort: a failure must not fail the delete.
	if err := s.db.DeleteChannelUsage(id); err != nil {
		s.log.Warn("delete channel usage failed", "channel", id, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminResetChannelQuota clears the current period counter of one
// channel, letting an operator recover a channel that hit its limit.
func (s *Server) handleAdminResetChannelQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snapshot := s.store.Snapshot()
	var channel *store.Channel
	for i := range snapshot.Channels {
		if snapshot.Channels[i].ID == id {
			ch := snapshot.Channels[i]
			channel = &ch
			break
		}
	}
	if channel == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	period := s.cfg.QuotaPeriod(time.Now())
	if err := s.db.ResetChannelUsage(period, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"quota": channelQuotaDTO(*channel, s.loadQuotaSnapshot()),
	})
}

func (s *Server) handleAdminTestChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
		URL   string `json:"url"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Query) == "" && strings.TrimSpace(in.URL) == "" {
		writeError(w, http.StatusBadRequest, "query or url is required")
		return
	}
	snapshot := s.store.Snapshot()
	var channel *store.Channel
	for i := range snapshot.Channels {
		if snapshot.Channels[i].ID == r.PathValue("id") {
			ch := snapshot.Channels[i]
			channel = &ch
			break
		}
	}
	if channel == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	start := time.Now()
	result, err := s.engine.TestChannel(r.Context(), *channel, strings.TrimSpace(in.Query), strings.TrimSpace(in.URL))
	entry := logstore.Entry{
		Protocol:    "admin-test",
		Time:        start.UTC(),
		Query:       in.Query,
		URL:         in.URL,
		Status:      http.StatusOK,
		ResultCount: 0,
		Channels:    []string{channel.Name},
		RouteName:   "admin-test",
		DurationMs:  time.Since(start).Milliseconds(),
		RemoteAddr:  r.RemoteAddr,
		UserAgent:   r.UserAgent(),
		RequestID:   requestIDFrom(r),
	}
	if err != nil {
		entry.Status = http.StatusBadGateway
		entry.Error = err.Error()
	}
	if appendErr := s.db.InsertLog(entry); appendErr != nil {
		s.log.Warn("append admin-test log failed", "err", appendErr)
	}
	check := summarizeCheck(strings.TrimSpace(in.Query), strings.TrimSpace(in.URL), result, time.Since(start), err)
	s.recordChannelCheck(channel.ID, check.Kind, check)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"latency_ms": entry.DurationMs,
		"check":      check,
		"result":     result,
	})
}

type routeInput struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Role            string   `json:"role"`
	Mode            string   `json:"mode"`
	Balance         string   `json:"balance"`
	MaxRetries      int      `json:"max_retries"`
	FallbackOnEmpty *bool    `json:"fallback_on_empty"`
	SearchChannels  []string `json:"search_channels"`
	FetchChannels   []string `json:"fetch_channels"`
	SearchGroups    []string `json:"search_groups"`
	FetchGroups     []string `json:"fetch_groups"`
	SearchOrder     []string `json:"search_order"`
	FetchOrder      []string `json:"fetch_order"`
	Enabled         *bool    `json:"enabled"`
}

func (s *Server) handleAdminListRoutes(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"routes": s.routeDTOs(snapshot)})
}

func (s *Server) handleAdminCreateRoute(w http.ResponseWriter, r *http.Request) {
	var in routeInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	fallback := false
	if in.FallbackOnEmpty != nil {
		fallback = *in.FallbackOnEmpty
	}
	created, err := s.store.CreateRoute(store.Route{
		Name: in.Name, Description: in.Description, Role: in.Role, Mode: in.Mode, Balance: in.Balance,
		MaxRetries: in.MaxRetries, FallbackOnEmpty: fallback, SearchChannels: in.SearchChannels,
		FetchChannels: in.FetchChannels, SearchGroups: in.SearchGroups, FetchGroups: in.FetchGroups,
		SearchOrder: in.SearchOrder, FetchOrder: in.FetchOrder, Enabled: enabled,
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, routeDTO(created))
}

func (s *Server) handleAdminUpdateRoute(w http.ResponseWriter, r *http.Request) {
	var in routeInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	snapshot := s.store.Snapshot()
	var current *store.Route
	for i := range snapshot.Routes {
		if snapshot.Routes[i].ID == r.PathValue("id") {
			route := snapshot.Routes[i]
			current = &route
			break
		}
	}
	if current == nil {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	enabled := current.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	fallback := current.FallbackOnEmpty
	if in.FallbackOnEmpty != nil {
		fallback = *in.FallbackOnEmpty
	}
	mode := in.Mode
	if mode == "" {
		mode = current.Mode
	}
	balance := in.Balance
	if balance == "" {
		balance = current.Balance
	}
	role := in.Role
	if role == "" {
		role = current.Role
	}
	updated, err := s.store.UpdateRoute(r.PathValue("id"), store.Route{
		Name: in.Name, Description: in.Description, Role: role, Mode: mode, Balance: balance,
		MaxRetries: in.MaxRetries, FallbackOnEmpty: fallback, SearchChannels: in.SearchChannels,
		FetchChannels: in.FetchChannels, SearchGroups: in.SearchGroups, FetchGroups: in.FetchGroups,
		SearchOrder: in.SearchOrder, FetchOrder: in.FetchOrder, Enabled: enabled,
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, routeDTO(updated))
}

func (s *Server) handleAdminDeleteRoute(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRoute(r.PathValue("id")); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminListKeys(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"keys": s.keyDTOs(snapshot)})
}

func (s *Server) handleAdminCreateKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string `json:"name"`
		RouteID       string `json:"route_id"`
		SearchRouteID string `json:"search_route_id"`
		FetchRouteID  string `json:"fetch_route_id"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	searchRouteID := in.SearchRouteID
	if searchRouteID == "" {
		searchRouteID = in.RouteID
	}
	key, token, err := s.store.CreateAPIKey(in.Name, searchRouteID, in.FetchRouteID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	snapshot := s.store.Snapshot()
	routeNames := make(map[string]string, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		routeNames[route.ID] = route.Name
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":   s.keyDTO(key, routeNames, s.keyStatsSnapshot()),
		"token": token,
	})
}

func (s *Server) handleAdminTestKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
		URL   string `json:"url"`
		Count int    `json:"count"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	query := strings.TrimSpace(in.Query)
	rawURL := strings.TrimSpace(in.URL)
	if query == "" && rawURL == "" {
		writeError(w, http.StatusBadRequest, "query or url is required")
		return
	}
	snapshot := s.store.Snapshot()
	var key *store.APIKey
	for i := range snapshot.APIKeys {
		if snapshot.APIKeys[i].ID == r.PathValue("id") {
			k := snapshot.APIKeys[i]
			key = &k
			break
		}
	}
	if key == nil {
		writeError(w, http.StatusNotFound, "API key not found")
		return
	}
	routeID := key.RouteID
	roleLabel := "search"
	if query == "" {
		routeID = key.FetchRouteID
		roleLabel = "fetch"
	}
	if routeID == "" {
		writeError(w, http.StatusConflict, "API key has no "+roleLabel+" route")
		return
	}
	var route *store.Route
	for i := range snapshot.Routes {
		if snapshot.Routes[i].ID == routeID {
			rt := snapshot.Routes[i]
			route = &rt
			break
		}
	}
	if route == nil {
		writeError(w, http.StatusConflict, "API key route is missing")
		return
	}
	if !key.Enabled {
		writeError(w, http.StatusConflict, "API key is disabled")
		return
	}
	if !route.Enabled {
		writeError(w, http.StatusConflict, "route "+route.Name+" is disabled")
		return
	}

	start := time.Now()
	var meta routing.Meta
	var results []search.Result
	var fetched *search.FetchResult
	var callErr error
	if query != "" {
		count := in.Count
		if count <= 0 {
			count = s.cfg.DefaultCount
		}
		if count < 1 {
			count = 1
		}
		if count > 50 {
			count = 50
		}
		results, meta, callErr = s.engine.Search(r.Context(), *route, search.SearchRequest{Query: query, Count: count})
	} else {
		fetched, meta, callErr = s.engine.Fetch(r.Context(), *route, rawURL)
	}
	duration := time.Since(start)

	entry := logstore.Entry{
		Protocol:     "key-test",
		Time:         start.UTC(),
		APIKeyID:     key.ID,
		APIKeyName:   key.Name,
		APIKeyPrefix: key.Prefix,
		RouteID:      meta.RouteID,
		RouteName:    meta.RouteName,
		Mode:         meta.Mode,
		Channels:     meta.Channels,
		Query:        query,
		URL:          rawURL,
		DurationMs:   duration.Milliseconds(),
		RemoteAddr:   r.RemoteAddr,
		UserAgent:    r.UserAgent(),
		RequestID:    requestIDFrom(r),
	}
	if callErr != nil {
		entry.Status = gatewayStatus(callErr)
		entry.Error = callErr.Error()
	} else {
		entry.Status = http.StatusOK
	}
	if query != "" {
		entry.ResultCount = len(results)
	}
	if appendErr := s.db.InsertLog(entry); appendErr != nil {
		s.log.Warn("append key-test log failed", "err", appendErr)
	}
	if callErr != nil {
		writeError(w, gatewayStatus(callErr), callErr.Error())
		return
	}

	response := map[string]any{
		"ok":          true,
		"duration_ms": duration.Milliseconds(),
		"route": map[string]any{
			"id":       meta.RouteID,
			"name":     meta.RouteName,
			"mode":     meta.Mode,
			"channels": meta.Channels,
		},
	}
	if query != "" {
		response["results"] = toResultJSON(results)
		response["query"] = query
	} else {
		response["fetch"] = map[string]any{
			"url":          fetched.URL,
			"title":        fetched.Title,
			"content":      fetched.Content,
			"content_type": fetched.ContentType,
			"status_code":  fetched.StatusCode,
		}
		response["url"] = rawURL
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleAdminUpdateKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          *string `json:"name"`
		RouteID       *string `json:"route_id"`
		SearchRouteID *string `json:"search_route_id"`
		FetchRouteID  *string `json:"fetch_route_id"`
		Enabled       *bool   `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	searchRouteID := in.SearchRouteID
	if searchRouteID == nil {
		searchRouteID = in.RouteID
	}
	key, err := s.store.UpdateAPIKey(r.PathValue("id"), in.Name, searchRouteID, in.FetchRouteID, in.Enabled)
	if err != nil {
		s.storeError(w, err)
		return
	}
	snapshot := s.store.Snapshot()
	routeNames := make(map[string]string, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		routeNames[route.ID] = route.Name
	}
	writeJSON(w, http.StatusOK, s.keyDTO(key, routeNames, s.keyStatsSnapshot()))
}

func (s *Server) handleAdminDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAPIKey(r.PathValue("id")); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminListLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	filter := logstore.Filter{
		Limit:    limit,
		Offset:   offset,
		Protocol: query.Get("protocol"),
		Status:   query.Get("status"),
		KeyID:    query.Get("key_id"),
		RouteID:  query.Get("route_id"),
		Channel:  query.Get("channel"),
		Search:   query.Get("q"),
	}
	if raw := query.Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.Since = t
		}
	}
	if raw := query.Get("until"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.Until = t
		}
	}
	entries, total, err := s.db.ListLogs(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
		"limit":   filter.Limit,
		"offset":  filter.Offset,
	})
}

func (s *Server) handleAdminClearLogs(w http.ResponseWriter, _ *http.Request) {
	if err := s.db.ClearLogs(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
