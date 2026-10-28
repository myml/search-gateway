// Package server exposes the gateway and management HTTP surfaces.
package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/logstore"
	"search-gateway/internal/routing"
	"search-gateway/internal/store"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxCall
)

// callContext is the resolved API key and routes for one request. Route is the
// role-specific route selected for ordinary gateway handlers; SearchRoute and
// FetchRoute are both populated for MCP, where one call may use either role.
type callContext struct {
	Key         store.APIKey
	Route       store.Route
	SearchRoute store.Route
	FetchRoute  store.Route
}

// KeyStats is the usage counter of one API key (stored in SQLite).
type KeyStats = db.KeyStats

// Server owns the HTTP routes, the store and the routing engine.
type Server struct {
	cfg         *config.Config
	store       *store.Store
	engine      *routing.Engine
	db          *db.DB
	log         *slog.Logger
	adminTokens []string
	mux         *http.ServeMux
}

// New creates the HTTP server.
func New(cfg *config.Config, st *store.Store, engine *routing.Engine, database *db.DB, log *slog.Logger) *Server {
	s := &Server{
		cfg:         cfg,
		store:       st,
		engine:      engine,
		db:          database,
		log:         log,
		adminTokens: cfg.AdminTokens,
		mux:         http.NewServeMux(),
	}
	if len(s.adminTokens) == 0 {
		s.adminTokens = cfg.Auth.Tokens // legacy configuration
	}
	if len(s.adminTokens) == 0 {
		log.Warn("admin API is unauthenticated: set admin_tokens")
	}
	s.routes()
	return s
}

// Handler returns the middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	return s.withRequestID(s.logging(s.mux))
}

func (s *Server) routes() {
	// Public console and health.
	s.mux.HandleFunc("GET /", s.handleRoot)
	s.mux.HandleFunc("GET /assets/app.js", s.handleAppJS)
	s.mux.HandleFunc("GET /assets/app.css", s.handleAppCSS)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)

	// Gateway surface, authenticated by issued API keys.
	s.mux.Handle("GET /api/info", s.withAPIKey(http.HandlerFunc(s.handleInfo)))
	s.mux.Handle("GET /search", s.withAPIKey(http.HandlerFunc(s.handleGetSearch)))
	s.mux.Handle("GET /fetch", s.withAPIKey(http.HandlerFunc(s.handleGetFetch)))
	s.mux.Handle("POST /v1/tools/search", s.withAPIKey(http.HandlerFunc(s.handleSearchAPI)))
	s.mux.Handle("POST /v1/tools/fetch", s.withAPIKey(http.HandlerFunc(s.handleFetchAPI)))
	s.mux.Handle("POST /v1/messages", s.withAPIKey(http.HandlerFunc(s.handleAnthropicMessages)))
	s.mux.Handle("POST /anthropic/v1/messages", s.withAPIKey(http.HandlerFunc(s.handleAnthropicMessages)))
	s.mux.Handle("POST /mcp", s.withMCPKey(http.HandlerFunc(s.handleMCP)))

	// Management surface, authenticated by admin tokens.
	s.mux.Handle("GET /admin/api/overview", s.withAdmin(http.HandlerFunc(s.handleAdminOverview)))
	s.mux.Handle("GET /admin/api/channels", s.withAdmin(http.HandlerFunc(s.handleAdminListChannels)))
	s.mux.Handle("POST /admin/api/channels", s.withAdmin(http.HandlerFunc(s.handleAdminCreateChannel)))
	s.mux.Handle("PATCH /admin/api/channels/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminUpdateChannel)))
	s.mux.Handle("DELETE /admin/api/channels/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminDeleteChannel)))
	s.mux.Handle("POST /admin/api/channels/{id}/test", s.withAdmin(http.HandlerFunc(s.handleAdminTestChannel)))
	s.mux.Handle("POST /admin/api/channels/test", s.withAdmin(http.HandlerFunc(s.handleAdminTestChannels)))
	s.mux.Handle("GET /admin/api/routes", s.withAdmin(http.HandlerFunc(s.handleAdminListRoutes)))
	s.mux.Handle("POST /admin/api/routes", s.withAdmin(http.HandlerFunc(s.handleAdminCreateRoute)))
	s.mux.Handle("PATCH /admin/api/routes/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminUpdateRoute)))
	s.mux.Handle("DELETE /admin/api/routes/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminDeleteRoute)))
	s.mux.Handle("GET /admin/api/keys", s.withAdmin(http.HandlerFunc(s.handleAdminListKeys)))
	s.mux.Handle("POST /admin/api/keys", s.withAdmin(http.HandlerFunc(s.handleAdminCreateKey)))
	s.mux.Handle("PATCH /admin/api/keys/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminUpdateKey)))
	s.mux.Handle("POST /admin/api/keys/{id}/test", s.withAdmin(http.HandlerFunc(s.handleAdminTestKey)))
	s.mux.Handle("DELETE /admin/api/keys/{id}", s.withAdmin(http.HandlerFunc(s.handleAdminDeleteKey)))
	s.mux.Handle("GET /admin/api/logs", s.withAdmin(http.HandlerFunc(s.handleAdminListLogs)))
	s.mux.Handle("POST /admin/api/logs/clear", s.withAdmin(http.HandlerFunc(s.handleAdminClearLogs)))
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" {
			id = randomID(8)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func (s *Server) withAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		key, searchRoute, fetchRoute, err := s.store.VerifyKey(token)
		if err != nil {
			s.logAuthFailure(r, token, err)
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		role := callRole(r)
		route := searchRoute
		if role == "fetch" {
			route = fetchRoute
		}
		if route.ID == "" {
			err := fmt.Errorf("%w: key has no %s route", store.ErrUnauthorized, role)
			s.logAuthFailure(r, token, err)
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if !route.Enabled {
			err := fmt.Errorf("%w: route %q is disabled", store.ErrUnauthorized, route.Name)
			s.logAuthFailure(r, token, err)
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if statErr := s.db.RecordKeyAttempt(key.ID, nil); statErr != nil {
			s.log.Warn("record key usage failed", "key", key.ID, "err", statErr)
		}
		ctx := context.WithValue(r.Context(), ctxCall, callContext{
			Key:         key,
			Route:       route,
			SearchRoute: searchRoute,
			FetchRoute:  fetchRoute,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withMCPKey authenticates an MCP request without forcing a single role: the
// key's search and fetch routes are both carried in the context so the MCP
// dispatcher can expose the tools the key actually covers.
func (s *Server) withMCPKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		key, searchRoute, fetchRoute, err := s.store.VerifyKey(token)
		if err != nil {
			s.logAuthFailure(r, token, err)
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		searchOK := routeUsable(searchRoute)
		fetchOK := routeUsable(fetchRoute)
		if !searchOK && !fetchOK {
			err := fmt.Errorf("%w: key has no enabled search or fetch route", store.ErrUnauthorized)
			s.logAuthFailure(r, token, err)
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if statErr := s.db.RecordKeyAttempt(key.ID, nil); statErr != nil {
			s.log.Warn("record key usage failed", "key", key.ID, "err", statErr)
		}
		route := searchRoute
		if !searchOK {
			route = fetchRoute
		}
		ctx := context.WithValue(r.Context(), ctxCall, callContext{
			Key:         key,
			Route:       route,
			SearchRoute: searchRoute,
			FetchRoute:  fetchRoute,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func callRole(r *http.Request) string {
	switch r.URL.Path {
	case "/fetch", "/v1/tools/fetch":
		return "fetch"
	default:
		return "search"
	}
}

func (s *Server) withAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.adminTokens) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		token := bearerToken(r)
		for _, candidate := range s.adminTokens {
			if subtle.ConstantTimeCompare([]byte(token), []byte(candidate)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeError(w, http.StatusUnauthorized, "invalid admin token")
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", requestIDFrom(r),
		)
	})
}

func (s *Server) logAuthFailure(r *http.Request, token string, err error) {
	entry := logstore.Entry{
		Protocol:   "auth",
		Status:     http.StatusUnauthorized,
		Error:      err.Error(),
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.UserAgent(),
		RequestID:  requestIDFrom(r),
	}
	if token != "" {
		entry.APIKeyPrefix = store.PrefixOf(token)
	}
	if appendErr := s.db.InsertLog(entry); appendErr != nil {
		s.log.Warn("append auth log failed", "err", appendErr)
	}
}

// recordCall persists one gateway call. Callers set protocol-specific fields
// (query/url/channels/result count) before calling.
func (s *Server) recordCall(r *http.Request, call callContext, entry logstore.Entry, start time.Time, callErr error) {
	entry.Time = start.UTC()
	entry.DurationMs = time.Since(start).Milliseconds()
	entry.RemoteAddr = r.RemoteAddr
	entry.UserAgent = r.UserAgent()
	entry.RequestID = requestIDFrom(r)
	entry.APIKeyID = call.Key.ID
	entry.APIKeyName = call.Key.Name
	entry.APIKeyPrefix = call.Key.Prefix
	entry.RouteID = call.Route.ID
	entry.RouteName = call.Route.Name
	if entry.Mode == "" {
		entry.Mode = call.Route.Mode
	}
	if callErr != nil {
		entry.Status = gatewayStatus(callErr)
		entry.Error = callErr.Error()
		s.recordKeyError(call.Key.ID, callErr)
	} else if entry.Status == 0 {
		entry.Status = http.StatusOK
	}
	if err := s.db.InsertLog(entry); err != nil {
		s.log.Warn("append call log failed", "err", err)
	}
}

func requestIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(ctxRequestID).(string)
	return id
}

func callFrom(r *http.Request) callContext {
	call, _ := r.Context().Value(ctxCall).(callContext)
	return call
}

func bearerToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-api-key")); v != "" {
		return v
	}
	if v := r.Header.Get("Authorization"); len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}
