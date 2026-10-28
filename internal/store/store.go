// Package store persists channels, routes and API keys in an atomically
// rewritten JSON document. It is the management-plane source of truth.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"search-gateway/internal/config"
)

const (
	// SchemaVersion is the store document version.
	SchemaVersion = 1
	// TokenPrefix prefixes every generated gateway API key.
	TokenPrefix = "sg_"
)

var (
	// ErrNotFound means the entity id does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means a name is already taken.
	ErrConflict = errors.New("already exists")
	// ErrInvalid means the submitted entity fails validation.
	ErrInvalid = errors.New("invalid")
	// ErrUnauthorized means the presented API key is missing, disabled or unroutable.
	ErrUnauthorized = errors.New("invalid api key")
)

var searchChannelTypes = map[string]struct{}{
	"tavily": {}, "brave": {}, "exa": {}, "serper": {}, "bocha": {}, "searxng": {},
}

var fetchChannelTypes = map[string]struct{}{
	"direct": {}, "jina": {},
}

var channelTypes = func() map[string]struct{} {
	out := map[string]struct{}{}
	for t := range searchChannelTypes {
		out[t] = struct{}{}
	}
	for t := range fetchChannelTypes {
		out[t] = struct{}{}
	}
	return out
}()

// Channel is one configured search/fetch upstream.
type Channel struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Config  config.Provider `json:"config"`
	Enabled bool            `json:"enabled"`
	// MonthlyQuota caps how many upstream calls this channel may serve per
	// calendar month. Zero means unlimited. Usage is counted in SQLite, and a
	// channel at its quota is skipped by the routing engine until the next
	// period starts, so this field never flips Enabled.
	MonthlyQuota int64     `json:"monthly_quota,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Capabilities lists the roles this channel can serve.
func (c Channel) Capabilities() []string {
	caps := make([]string, 0, 2)
	if c.Supports("search") {
		caps = append(caps, "search")
	}
	if c.Supports("fetch") {
		caps = append(caps, "fetch")
	}
	return caps
}

// Supports reports whether the channel type serves the role.
func (c Channel) Supports(role string) bool {
	if role != "search" && role != "fetch" {
		return false
	}
	if role == "search" {
		_, ok := searchChannelTypes[c.Type]
		return ok
	}
	_, ok := fetchChannelTypes[c.Type]
	return ok
}

// Route is the "model-like" routing abstraction: an ordered channel set plus a
// selection mode. Mode is "fallback" (try in order, first success wins) or
// "merge" (run all, merge and de-duplicate results).
type Route struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Role            string    `json:"role"`
	Description     string    `json:"description"`
	Mode            string    `json:"mode"`
	Balance         string    `json:"balance,omitempty"`
	MaxRetries      int       `json:"max_retries,omitempty"`
	FallbackOnEmpty bool      `json:"fallback_on_empty"`
	SearchChannels  []string  `json:"search_channels"`
	FetchChannels   []string  `json:"fetch_channels"`
	SearchGroups    []string  `json:"search_groups,omitempty"`
	FetchGroups     []string  `json:"fetch_groups,omitempty"`
	SearchOrder     []string  `json:"search_order,omitempty"`
	FetchOrder      []string  `json:"fetch_order,omitempty"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// APIKey is a client credential bound to a search route and/or a fetch route.
// Only its hash is stored. RouteID is the search route (legacy name kept for
// JSON compatibility); FetchRouteID is optional.
type APIKey struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Prefix       string    `json:"prefix"`
	Hash         string    `json:"hash"`
	RouteID      string    `json:"route_id"`
	FetchRouteID string    `json:"fetch_route_id,omitempty"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Data is the persisted document.
type Data struct {
	Version   int       `json:"version"`
	Revision  int64     `json:"revision"`
	Channels  []Channel `json:"channels"`
	Routes    []Route   `json:"routes"`
	APIKeys   []APIKey  `json:"api_keys"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is a concurrency-safe JSON-backed store.
type Store struct {
	mu   sync.RWMutex
	path string
	data Data
}

// Open loads the store, creating an empty one when the file does not exist.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store path is required")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create store dir: %w", err)
		}
	}
	s := &Store{path: path, data: Data{Version: SchemaVersion}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read store %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("parse store %s: %w", path, err)
	}
	if s.data.Version == 0 {
		s.data.Version = SchemaVersion
	}
	if migrateData(&s.data) {
		s.data.Version = SchemaVersion
		s.data.Revision++
		s.data.UpdatedAt = time.Now().UTC()
		if err := writeAtomic(s.path, s.data); err != nil {
			return nil, fmt.Errorf("persist store migration: %w", err)
		}
	}
	return s, nil
}

// migrateData upgrades legacy stores in place:
//   - generic channels (removed from the product) and their route references
//     are dropped,
//   - each pre-role route becomes a search route or a fetch route,
//   - a route that had both roles is split, keeping the original id for search,
//   - existing keys are bound to both halves.
func migrateData(d *Data) bool {
	changed := dropLegacyGenericChannels(d)
	oldToFetch := map[string]string{}
	var extraRoutes []Route
	for i := range d.Routes {
		route := &d.Routes[i]
		if route.Role == "search" || route.Role == "fetch" {
			continue
		}
		hasSearch := hasRoleRefs(route, "search")
		hasFetch := hasRoleRefs(route, "fetch")
		switch {
		case hasSearch && hasFetch:
			route.Role = "search"
			fetchRoute := *route
			fetchRoute.ID = newID("rt")
			fetchRoute.Role = "fetch"
			fetchRoute.Name = uniqueRouteName(d, route.Name+" · 抓取")
			fetchRoute.Description = strings.TrimSpace(route.Description)
			fetchRoute.SearchChannels = nil
			fetchRoute.SearchGroups = nil
			fetchRoute.SearchOrder = nil
			route.FetchChannels = nil
			route.FetchGroups = nil
			route.FetchOrder = nil
			extraRoutes = append(extraRoutes, fetchRoute)
			oldToFetch[route.ID] = fetchRoute.ID
		case hasFetch:
			route.Role = "fetch"
			route.SearchChannels = nil
			route.SearchGroups = nil
			route.SearchOrder = nil
		default:
			route.Role = "search"
			route.FetchChannels = nil
			route.FetchGroups = nil
			route.FetchOrder = nil
		}
		changed = true
	}
	if changed {
		d.Routes = append(d.Routes, extraRoutes...)
		for i := range d.Routes {
			migrateRouteRefs(&d.Routes[i], "search", d, oldToFetch)
			migrateRouteRefs(&d.Routes[i], "fetch", d, oldToFetch)
		}
		for i := range d.APIKeys {
			key := &d.APIKeys[i]
			if key.RouteID == "" && key.FetchRouteID == "" {
				continue
			}
			oldID := key.RouteID
			route, ok := findRoute(d, oldID)
			if !ok {
				continue
			}
			switch route.Role {
			case "fetch":
				key.FetchRouteID = route.ID
				key.RouteID = ""
			case "search":
				if fetchID, ok := oldToFetch[oldID]; ok {
					key.FetchRouteID = fetchID
				}
			}
		}
	}
	return changed
}

// dropLegacyGenericChannels removes generic channels and their route targets.
// The generic adapter was removed because it made the channel model and UI far
// more complex than the fixed providers warrant.
func dropLegacyGenericChannels(d *Data) bool {
	removed := map[string]struct{}{}
	kept := make([]Channel, 0, len(d.Channels))
	for _, ch := range d.Channels {
		if ch.Type == "generic" {
			removed[ch.ID] = struct{}{}
			continue
		}
		kept = append(kept, ch)
	}
	if len(removed) == 0 {
		return false
	}
	d.Channels = kept
	for i := range d.Routes {
		route := &d.Routes[i]
		for id := range removed {
			route.SearchChannels = removeID(route.SearchChannels, id)
			route.FetchChannels = removeID(route.FetchChannels, id)
			route.SearchOrder = removeID(route.SearchOrder, id)
			route.FetchOrder = removeID(route.FetchOrder, id)
		}
	}
	return true
}

func uniqueRouteName(d *Data, base string) string {
	name := base
	for i := 2; ; i++ {
		taken := false
		for _, route := range d.Routes {
			if strings.EqualFold(route.Name, name) {
				taken = true
				break
			}
		}
		if !taken {
			return name
		}
		name = fmt.Sprintf("%s %d", base, i)
	}
}

func migrateRouteRefs(route *Route, role string, d *Data, oldToFetch map[string]string) {
	if route.Role != role {
		return
	}
	channels, groups, order := route.SearchChannels, route.SearchGroups, route.SearchOrder
	if role == "fetch" {
		channels, groups, order = route.FetchChannels, route.FetchGroups, route.FetchOrder
	}
	refs := dedupe(order)
	if len(refs) == 0 {
		refs = append(append([]string{}, channels...), groups...)
	}
	outChannels := make([]string, 0, len(refs))
	outGroups := make([]string, 0, len(refs))
	for _, ref := range refs {
		if role == "fetch" {
			if mapped, ok := oldToFetch[ref]; ok {
				ref = mapped
			}
		}
		if ch, ok := findChannel(d, ref); ok {
			if ch.Supports(role) {
				outChannels = append(outChannels, ref)
			}
			continue
		}
		if child, ok := findRoute(d, ref); ok && child.Role == role {
			outGroups = append(outGroups, ref)
		}
	}
	if role == "search" {
		route.SearchChannels, route.SearchGroups, route.SearchOrder = outChannels, outGroups, append(append([]string{}, outChannels...), outGroups...)
	} else {
		route.FetchChannels, route.FetchGroups, route.FetchOrder = outChannels, outGroups, append(append([]string{}, outChannels...), outGroups...)
	}
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// Snapshot returns a deep copy of the current document.
func (s *Store) Snapshot() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return deepCopy(s.data)
}

// Revision returns the current document revision.
func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Revision
}

func (s *Store) mutate(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := deepCopy(s.data)
	if err := fn(&next); err != nil {
		return err
	}
	next.Version = SchemaVersion
	next.Revision = s.data.Revision + 1
	next.UpdatedAt = time.Now().UTC()
	if err := writeAtomic(s.path, next); err != nil {
		return err
	}
	s.data = next
	return nil
}

func writeAtomic(path string, data Data) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".store-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp store: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp store: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace store: %w", err)
	}
	return nil
}

func deepCopy(d Data) Data {
	out := Data{
		Version:   d.Version,
		Revision:  d.Revision,
		UpdatedAt: d.UpdatedAt,
		Channels:  make([]Channel, len(d.Channels)),
		Routes:    make([]Route, len(d.Routes)),
		APIKeys:   make([]APIKey, len(d.APIKeys)),
	}
	for i, ch := range d.Channels {
		ch.Config = copyProvider(ch.Config)
		out.Channels[i] = ch
	}
	for i, r := range d.Routes {
		r.SearchChannels = append([]string(nil), r.SearchChannels...)
		r.FetchChannels = append([]string(nil), r.FetchChannels...)
		r.SearchGroups = append([]string(nil), r.SearchGroups...)
		r.FetchGroups = append([]string(nil), r.FetchGroups...)
		r.SearchOrder = append([]string(nil), r.SearchOrder...)
		r.FetchOrder = append([]string(nil), r.FetchOrder...)
		out.Routes[i] = r
	}
	copy(out.APIKeys, d.APIKeys)
	return out
}

func copyProvider(p config.Provider) config.Provider {
	if p.Headers != nil {
		headers := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			headers[k] = v
		}
		p.Headers = headers
	}
	if p.InsecureSkipVerify != nil {
		v := *p.InsecureSkipVerify
		p.InsecureSkipVerify = &v
	}
	return p
}

// CreateChannel adds a channel.
func (s *Store) CreateChannel(ch Channel) (Channel, error) {
	now := time.Now().UTC()
	err := s.mutate(func(d *Data) error {
		ch.ID = newID("ch")
		ch.CreatedAt = now
		ch.UpdatedAt = now
		if err := validateChannel(&ch, d); err != nil {
			return err
		}
		d.Channels = append(d.Channels, ch)
		return nil
	})
	return ch, err
}

// UpdateChannel replaces the editable fields of a channel. An empty API key
// keeps the stored one; the marker "__clear__" removes it.
func (s *Store) UpdateChannel(id string, patch Channel) (Channel, error) {
	var updated Channel
	err := s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.Channels {
			if d.Channels[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		current := d.Channels[idx]
		updated = current
		updated.Name = patch.Name
		updated.Type = patch.Type
		updated.Enabled = patch.Enabled
		updated.Config = patch.Config
		updated.MonthlyQuota = patch.MonthlyQuota
		if strings.TrimSpace(updated.Config.APIKey) == "" {
			updated.Config.APIKey = current.Config.APIKey
		} else if updated.Config.APIKey == "__clear__" {
			updated.Config.APIKey = ""
		}
		updated.UpdatedAt = time.Now().UTC()
		if err := validateChannel(&updated, d); err != nil {
			return err
		}
		d.Channels[idx] = updated
		return nil
	})
	return updated, err
}

// DeleteChannel removes a channel and every route reference to it.
func (s *Store) DeleteChannel(id string) error {
	return s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.Channels {
			if d.Channels[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		d.Channels = append(d.Channels[:idx], d.Channels[idx+1:]...)
		for i := range d.Routes {
			d.Routes[i].SearchChannels = removeID(d.Routes[i].SearchChannels, id)
			d.Routes[i].FetchChannels = removeID(d.Routes[i].FetchChannels, id)
			d.Routes[i].SearchOrder = removeID(d.Routes[i].SearchOrder, id)
			d.Routes[i].FetchOrder = removeID(d.Routes[i].FetchOrder, id)
		}
		return nil
	})
}

func validateChannel(ch *Channel, d *Data) error {
	ch.Name = strings.TrimSpace(ch.Name)
	ch.Type = strings.TrimSpace(ch.Type)
	if ch.Name == "" {
		return fmt.Errorf("%w: channel name is required", ErrInvalid)
	}
	if _, ok := channelTypes[ch.Type]; !ok {
		return fmt.Errorf("%w: unknown channel type %q", ErrInvalid, ch.Type)
	}
	if ch.MonthlyQuota < 0 {
		return fmt.Errorf("%w: monthly_quota cannot be negative", ErrInvalid)
	}
	for _, other := range d.Channels {
		if other.ID != ch.ID && strings.EqualFold(other.Name, ch.Name) {
			return fmt.Errorf("%w: channel name %q", ErrConflict, ch.Name)
		}
	}
	switch ch.Type {
	case "tavily", "brave", "exa", "serper", "bocha":
		if ch.Config.APIKey == "" {
			return fmt.Errorf("%w: channel %q requires api_key", ErrInvalid, ch.Name)
		}
	case "searxng":
		if strings.TrimSpace(ch.Config.BaseURL) == "" {
			return fmt.Errorf("%w: channel %q requires base_url", ErrInvalid, ch.Name)
		}
	}
	ch.Config.Name = ch.Name
	ch.Config.Type = ch.Type
	return nil
}

// CreateRoute adds a route.
func (s *Store) CreateRoute(route Route) (Route, error) {
	now := time.Now().UTC()
	err := s.mutate(func(d *Data) error {
		route.ID = newID("rt")
		route.CreatedAt = now
		route.UpdatedAt = now
		if err := validateRoute(&route, d); err != nil {
			return err
		}
		d.Routes = append(d.Routes, route)
		return nil
	})
	return route, err
}

// UpdateRoute replaces the editable fields of a route.
func (s *Store) UpdateRoute(id string, patch Route) (Route, error) {
	var updated Route
	err := s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.Routes {
			if d.Routes[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		current := d.Routes[idx]
		updated = current
		if current.Role != "" && patch.Role != "" && patch.Role != current.Role {
			return fmt.Errorf("%w: route role cannot change", ErrInvalid)
		}
		updated.Name = patch.Name
		updated.Description = patch.Description
		updated.Mode = patch.Mode
		updated.Balance = patch.Balance
		updated.MaxRetries = patch.MaxRetries
		updated.FallbackOnEmpty = patch.FallbackOnEmpty
		updated.SearchChannels = append([]string(nil), patch.SearchChannels...)
		updated.FetchChannels = append([]string(nil), patch.FetchChannels...)
		updated.SearchGroups = append([]string(nil), patch.SearchGroups...)
		updated.FetchGroups = append([]string(nil), patch.FetchGroups...)
		updated.SearchOrder = append([]string(nil), patch.SearchOrder...)
		updated.FetchOrder = append([]string(nil), patch.FetchOrder...)
		updated.Enabled = patch.Enabled
		updated.UpdatedAt = time.Now().UTC()
		if err := validateRoute(&updated, d); err != nil {
			return err
		}
		d.Routes[idx] = updated
		return nil
	})
	return updated, err
}

// DeleteRoute removes an unreferenced route.
func (s *Store) DeleteRoute(id string) error {
	return s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.Routes {
			if d.Routes[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		var ids []string
		for _, k := range d.APIKeys {
			if k.RouteID == id || k.FetchRouteID == id {
				ids = append(ids, k.Name)
			}
		}
		if len(ids) > 0 {
			return fmt.Errorf("%w: route is used by api keys %s", ErrConflict, strings.Join(ids, ", "))
		}
		var groupUsers []string
		for _, other := range d.Routes {
			if other.ID == id {
				continue
			}
			for _, groupID := range append(append([]string{}, other.SearchGroups...), other.FetchGroups...) {
				if groupID == id {
					groupUsers = append(groupUsers, other.Name)
					break
				}
			}
		}
		if len(groupUsers) > 0 {
			return fmt.Errorf("%w: route is used as a group by routes %s", ErrConflict, strings.Join(groupUsers, ", "))
		}
		d.Routes = append(d.Routes[:idx], d.Routes[idx+1:]...)
		return nil
	})
}

func validateRoute(route *Route, d *Data) error {
	route.Name = strings.TrimSpace(route.Name)
	if route.Name == "" {
		return fmt.Errorf("%w: route name is required", ErrInvalid)
	}
	role := strings.TrimSpace(route.Role)
	if role == "" {
		hasSearch := hasRoleRefs(route, "search")
		hasFetch := hasRoleRefs(route, "fetch")
		switch {
		case hasSearch && !hasFetch:
			role = "search"
		case hasFetch && !hasSearch:
			role = "fetch"
		case !hasSearch && !hasFetch:
			role = "search"
		default:
			return fmt.Errorf("%w: route role is required when both search and fetch targets are set", ErrInvalid)
		}
	}
	if role != "search" && role != "fetch" {
		return fmt.Errorf("%w: route role must be search or fetch", ErrInvalid)
	}
	route.Role = role
	if route.Mode == "" {
		route.Mode = "fallback"
	}
	if route.Mode != "fallback" && route.Mode != "merge" && route.Mode != "balance" {
		return fmt.Errorf("%w: mode must be fallback, merge or balance", ErrInvalid)
	}
	if route.Mode == "balance" {
		if route.Balance == "" {
			route.Balance = "round_robin"
		}
		if route.Balance != "round_robin" && route.Balance != "random" {
			return fmt.Errorf("%w: balance strategy must be round_robin or random", ErrInvalid)
		}
	} else {
		route.Balance = ""
	}
	if route.MaxRetries < 0 {
		return fmt.Errorf("%w: max_retries cannot be negative", ErrInvalid)
	}
	for _, other := range d.Routes {
		if other.ID != route.ID && strings.EqualFold(other.Name, route.Name) {
			return fmt.Errorf("%w: route name %q", ErrConflict, route.Name)
		}
	}
	if role == "search" {
		route.FetchChannels = nil
		route.FetchGroups = nil
		route.FetchOrder = nil
	} else {
		route.SearchChannels = nil
		route.SearchGroups = nil
		route.SearchOrder = nil
	}
	if err := normalizeRoleTargets(route, d); err != nil {
		return err
	}
	switch role {
	case "search":
		for _, id := range route.SearchChannels {
			ch, ok := findChannel(d, id)
			if !ok {
				return fmt.Errorf("%w: search channel %q not found", ErrInvalid, id)
			}
			if !ch.Supports("search") {
				return fmt.Errorf("%w: channel %q cannot search", ErrInvalid, ch.Name)
			}
		}
		if err := validateRouteGroups(route, "search", route.SearchGroups, d); err != nil {
			return err
		}
	case "fetch":
		for _, id := range route.FetchChannels {
			ch, ok := findChannel(d, id)
			if !ok {
				return fmt.Errorf("%w: fetch channel %q not found", ErrInvalid, id)
			}
			if !ch.Supports("fetch") {
				return fmt.Errorf("%w: channel %q cannot fetch", ErrInvalid, ch.Name)
			}
		}
		if err := validateRouteGroups(route, "fetch", route.FetchGroups, d); err != nil {
			return err
		}
	}
	return nil
}

func hasRoleRefs(route *Route, role string) bool {
	if role == "search" {
		return len(route.SearchChannels)+len(route.SearchGroups)+len(route.SearchOrder) > 0
	}
	return len(route.FetchChannels)+len(route.FetchGroups)+len(route.FetchOrder) > 0
}

// normalizeRoleTargets treats the role's order field as the canonical ordered
// list of channels and groups. Legacy routes fall back to "channels first, then
// groups". The channel/group arrays are derived from the order.
func normalizeRoleTargets(route *Route, d *Data) error {
	if route.Role == "search" {
		return normalizeRouteOrder(&route.SearchChannels, &route.SearchGroups, &route.SearchOrder, d, "search")
	}
	return normalizeRouteOrder(&route.FetchChannels, &route.FetchGroups, &route.FetchOrder, d, "fetch")
}

func normalizeRouteOrder(channels, groups, order *[]string, d *Data, role string) error {
	*channels = dedupe(*channels)
	*groups = dedupe(*groups)
	*order = dedupe(*order)
	if len(*order) == 0 {
		*order = append(append([]string{}, (*channels)...), (*groups)...)
	}
	// Keep additions made only through the channel/group arrays visible too.
	for _, ref := range append(append([]string{}, (*channels)...), (*groups)...) {
		found := false
		for _, existing := range *order {
			if existing == ref {
				found = true
				break
			}
		}
		if !found {
			*order = append(*order, ref)
		}
	}
	chans := make([]string, 0, len(*order))
	grps := make([]string, 0, len(*order))
	for _, ref := range *order {
		if _, ok := findChannel(d, ref); ok {
			chans = append(chans, ref)
			continue
		}
		if _, ok := findRoute(d, ref); ok {
			grps = append(grps, ref)
			continue
		}
		return fmt.Errorf("%w: route target %q not found", ErrInvalid, ref)
	}
	*channels = chans
	*groups = grps
	return nil
}

func validateRouteGroups(route *Route, role string, groups []string, d *Data) error {
	for _, id := range groups {
		if id == route.ID {
			return fmt.Errorf("%w: route cannot reference itself as a group", ErrInvalid)
		}
		child, ok := findRoute(d, id)
		if !ok {
			return fmt.Errorf("%w: %s group route %q not found", ErrInvalid, role, id)
		}
		if child.Role != role {
			return fmt.Errorf("%w: group route %q is not a %s route", ErrInvalid, child.Name, role)
		}
		if !routeSupportsRole(child, role, d, map[string]bool{}) {
			return fmt.Errorf("%w: group route %q cannot %s", ErrInvalid, child.Name, role)
		}
		if routeReferencesTarget(child, route.ID, d, map[string]bool{}) {
			return fmt.Errorf("%w: group route %q creates a cycle", ErrInvalid, child.Name)
		}
	}
	return nil
}

func routeSupportsRole(route Route, role string, d *Data, seen map[string]bool) bool {
	if seen[route.ID] {
		return false
	}
	seen[route.ID] = true
	if route.Role != "" && route.Role != role {
		return false
	}
	channelIDs, groupIDs := route.SearchChannels, route.SearchGroups
	if role == "fetch" {
		channelIDs, groupIDs = route.FetchChannels, route.FetchGroups
	}
	for _, id := range channelIDs {
		if ch, ok := findChannel(d, id); ok && ch.Supports(role) {
			return true
		}
	}
	for _, id := range groupIDs {
		if child, ok := findRoute(d, id); ok && routeSupportsRole(child, role, d, seen) {
			return true
		}
	}
	return false
}

func routeReferencesTarget(route Route, targetID string, d *Data, seen map[string]bool) bool {
	if seen[route.ID] {
		return false
	}
	seen[route.ID] = true
	for _, id := range append(append([]string{}, route.SearchGroups...), route.FetchGroups...) {
		if id == targetID {
			return true
		}
		if child, ok := findRoute(d, id); ok && routeReferencesTarget(child, targetID, d, seen) {
			return true
		}
	}
	return false
}

// CreateAPIKey issues a new gateway key bound to a search route and/or a fetch
// route, and returns the plaintext token exactly once.
func (s *Store) CreateAPIKey(name, searchRouteID, fetchRouteID string) (APIKey, string, error) {
	token, err := GenerateToken()
	if err != nil {
		return APIKey{}, "", err
	}
	now := time.Now().UTC()
	var key APIKey
	err = s.mutate(func(d *Data) error {
		searchRoute, err := resolveRouteForRole(d, searchRouteID, "search")
		if err != nil {
			return err
		}
		fetchRoute, err := resolveRouteForRole(d, fetchRouteID, "fetch")
		if err != nil {
			return err
		}
		if searchRoute == nil && fetchRoute == nil {
			return fmt.Errorf("%w: select at least one search or fetch route", ErrInvalid)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = PrefixOf(token)
		}
		key = APIKey{
			ID: newID("key"), Name: name, Prefix: PrefixOf(token), Hash: HashToken(token),
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
		if searchRoute != nil {
			key.RouteID = searchRoute.ID
		}
		if fetchRoute != nil {
			key.FetchRouteID = fetchRoute.ID
		}
		d.APIKeys = append(d.APIKeys, key)
		return nil
	})
	return key, token, err
}

func resolveRouteForRole(d *Data, routeID, role string) (*Route, error) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil, nil
	}
	route, ok := findRoute(d, routeID)
	if !ok {
		return nil, fmt.Errorf("%w: %s route %q not found", ErrInvalid, role, routeID)
	}
	if route.Role != role {
		return nil, fmt.Errorf("%w: route %q is not a %s route", ErrInvalid, route.Name, role)
	}
	return &route, nil
}

// UpdateAPIKey changes a key's name, routes or enabled flag. Nil pointers keep
// the current value; empty route IDs clear that side.
func (s *Store) UpdateAPIKey(id string, name, searchRouteID, fetchRouteID *string, enabled *bool) (APIKey, error) {
	var updated APIKey
	err := s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.APIKeys {
			if d.APIKeys[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		current := d.APIKeys[idx]
		updated = current
		if name != nil {
			if strings.TrimSpace(*name) == "" {
				return fmt.Errorf("%w: key name is required", ErrInvalid)
			}
			updated.Name = strings.TrimSpace(*name)
		}
		if searchRouteID != nil {
			route, err := resolveRouteForRole(d, *searchRouteID, "search")
			if err != nil {
				return err
			}
			if route == nil {
				updated.RouteID = ""
			} else {
				updated.RouteID = route.ID
			}
		}
		if fetchRouteID != nil {
			route, err := resolveRouteForRole(d, *fetchRouteID, "fetch")
			if err != nil {
				return err
			}
			if route == nil {
				updated.FetchRouteID = ""
			} else {
				updated.FetchRouteID = route.ID
			}
		}
		if updated.RouteID == "" && updated.FetchRouteID == "" {
			return fmt.Errorf("%w: key needs at least one search or fetch route", ErrInvalid)
		}
		if enabled != nil {
			updated.Enabled = *enabled
		}
		updated.UpdatedAt = time.Now().UTC()
		d.APIKeys[idx] = updated
		return nil
	})
	return updated, err
}

// DeleteAPIKey revokes a key.
func (s *Store) DeleteAPIKey(id string) error {
	return s.mutate(func(d *Data) error {
		idx := -1
		for i := range d.APIKeys {
			if d.APIKeys[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		d.APIKeys = append(d.APIKeys[:idx], d.APIKeys[idx+1:]...)
		return nil
	})
}

// VerifyKey resolves a plaintext token to its key plus its search and fetch
// routes. A route may be zero when the key does not cover that role.
func (s *Store) VerifyKey(token string) (APIKey, Route, Route, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return APIKey{}, Route{}, Route{}, ErrUnauthorized
	}
	hash := HashToken(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.data.APIKeys {
		if key.Hash != hash {
			continue
		}
		if !key.Enabled {
			return APIKey{}, Route{}, Route{}, fmt.Errorf("%w: key is disabled", ErrUnauthorized)
		}
		var searchRoute, fetchRoute Route
		if key.RouteID != "" {
			route, ok := findRoute(&s.data, key.RouteID)
			if !ok {
				return APIKey{}, Route{}, Route{}, fmt.Errorf("%w: key search route is missing", ErrUnauthorized)
			}
			searchRoute = route
		}
		if key.FetchRouteID != "" {
			route, ok := findRoute(&s.data, key.FetchRouteID)
			if !ok {
				return APIKey{}, Route{}, Route{}, fmt.Errorf("%w: key fetch route is missing", ErrUnauthorized)
			}
			fetchRoute = route
		}
		if searchRoute.ID == "" && fetchRoute.ID == "" {
			return APIKey{}, Route{}, Route{}, fmt.Errorf("%w: key has no routes", ErrUnauthorized)
		}
		return key, searchRoute, fetchRoute, nil
	}
	return APIKey{}, Route{}, Route{}, ErrUnauthorized
}

// SeedFromConfig imports the legacy configuration into an empty store: every
// configured provider becomes a channel, a "default" route selects them all,
// and legacy auth tokens become API keys on that route.
func (s *Store) SeedFromConfig(cfg *config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Channels) > 0 || len(s.data.Routes) > 0 || len(s.data.APIKeys) > 0 {
		return nil
	}
	now := time.Now().UTC()
	next := deepCopy(s.data)
	byName := map[string]int{}
	addChannel := func(p config.Provider) {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = p.Type
		}
		if idx, ok := byName[strings.ToLower(name)]; ok {
			// The same name in both lists means one channel serving both roles.
			next.Channels[idx].UpdatedAt = now
			return
		}
		p.Name = name
		next.Channels = append(next.Channels, Channel{
			ID: newID("ch"), Name: name, Type: p.Type, Config: p,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		})
		byName[strings.ToLower(name)] = len(next.Channels) - 1
	}
	for _, p := range cfg.Search {
		addChannel(p)
	}
	for _, p := range cfg.Fetch {
		addChannel(p)
	}
	if len(next.Channels) == 0 {
		return nil
	}

	now = time.Now().UTC()
	var searchRoute, fetchRoute *Route
	var searchChannels, fetchChannels []string
	for _, ch := range next.Channels {
		if ch.Supports("search") {
			searchChannels = append(searchChannels, ch.ID)
		}
		if ch.Supports("fetch") {
			fetchChannels = append(fetchChannels, ch.ID)
		}
	}
	if len(searchChannels) > 0 {
		searchRoute = &Route{
			ID: newID("rt"), Name: "default", Role: "search", Description: "seeded from config",
			Mode: "fallback", FallbackOnEmpty: cfg.FallbackOnEmpty, Enabled: true,
			SearchChannels: searchChannels, SearchOrder: append([]string{}, searchChannels...),
			CreatedAt: now, UpdatedAt: now,
		}
		next.Routes = append(next.Routes, *searchRoute)
	}
	if len(fetchChannels) > 0 {
		name := "default"
		if searchRoute != nil {
			name = "default-fetch"
		}
		fetchRoute = &Route{
			ID: newID("rt"), Name: name, Role: "fetch", Description: "seeded from config",
			Mode: "fallback", FallbackOnEmpty: cfg.FallbackOnEmpty, Enabled: true,
			FetchChannels: fetchChannels, FetchOrder: append([]string{}, fetchChannels...),
			CreatedAt: now, UpdatedAt: now,
		}
		next.Routes = append(next.Routes, *fetchRoute)
	}
	if searchRoute == nil && fetchRoute == nil {
		return nil
	}

	for i, token := range cfg.Auth.Tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		key := APIKey{
			ID: newID("key"), Name: fmt.Sprintf("legacy-%d", i+1),
			Prefix: PrefixOf(token), Hash: HashToken(token), Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
		if searchRoute != nil {
			key.RouteID = searchRoute.ID
		}
		if fetchRoute != nil {
			key.FetchRouteID = fetchRoute.ID
		}
		next.APIKeys = append(next.APIKeys, key)
	}

	next.Version = SchemaVersion
	next.Revision = 1
	next.UpdatedAt = now
	if err := writeAtomic(s.path, next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// GenerateToken creates a new plaintext gateway token.
func GenerateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return TokenPrefix + hex.EncodeToString(buf), nil
}

// HashToken returns the storage hash of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PrefixOf returns a short display prefix for a token.
func PrefixOf(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12] + "…"
}

func newID(prefix string) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func findChannel(d *Data, id string) (Channel, bool) {
	for _, ch := range d.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return Channel{}, false
}

func findRoute(d *Data, id string) (Route, bool) {
	if id == "" {
		for _, r := range d.Routes {
			if r.Name == "default" {
				return r, true
			}
		}
		return Route{}, false
	}
	for _, r := range d.Routes {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}

func removeID(ids []string, id string) []string {
	out := ids[:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
