// Package logstore defines the call-log entry and filter types shared by the
// SQLite store and the HTTP API.
package logstore

import (
	"strings"
	"time"
)

// Entry is one recorded gateway call.
type Entry struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Protocol     string    `json:"protocol"`
	APIKeyID     string    `json:"api_key_id,omitempty"`
	APIKeyName   string    `json:"api_key_name,omitempty"`
	APIKeyPrefix string    `json:"api_key_prefix,omitempty"`
	RouteID      string    `json:"route_id,omitempty"`
	RouteName    string    `json:"route_name,omitempty"`
	Mode         string    `json:"mode,omitempty"`
	Channels     []string  `json:"channels,omitempty"`
	Query        string    `json:"query,omitempty"`
	URL          string    `json:"url,omitempty"`
	ResultCount  int       `json:"result_count,omitempty"`
	Status       int       `json:"status"`
	Error        string    `json:"error,omitempty"`
	DurationMs   int64     `json:"duration_ms"`
	RemoteAddr   string    `json:"remote_addr,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
}

// Filter selects entries for the admin API. Zero values mean "any".
type Filter struct {
	Limit    int
	Offset   int
	Protocol string
	Status   string // "ok", "error", or an exact HTTP status
	KeyID    string
	RouteID  string
	Channel  string
	Search   string
	Since    time.Time
	Until    time.Time
}

// Normalize applies default and maximum paging values.
func (f Filter) Normalize() Filter {
	f.Protocol = strings.TrimSpace(f.Protocol)
	f.Status = strings.TrimSpace(f.Status)
	f.KeyID = strings.TrimSpace(f.KeyID)
	f.RouteID = strings.TrimSpace(f.RouteID)
	f.Channel = strings.TrimSpace(f.Channel)
	f.Search = strings.TrimSpace(f.Search)
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}
