// Package db stores dynamic gateway data (call logs, channel usage, channel
// test results and API key usage) in SQLite. Static configuration (channels,
// routes, API keys) remains in the JSON store.
package db

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"search-gateway/internal/logstore"
)

// ChannelStats is the persisted usage counter of one channel.
type ChannelStats struct {
	SearchRequests int64      `json:"search_requests"`
	SearchErrors   int64      `json:"search_errors"`
	FetchRequests  int64      `json:"fetch_requests"`
	FetchErrors    int64      `json:"fetch_errors"`
	LastError      string     `json:"last_error,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}

// ChannelTest is the persisted result of the last connectivity test.
type ChannelTest struct {
	OK           bool      `json:"ok"`
	Kind         string    `json:"kind"`
	Query        string    `json:"query,omitempty"`
	URL          string    `json:"url,omitempty"`
	ResultCount  int       `json:"result_count"`
	ContentChars int       `json:"content_chars,omitempty"`
	Title        string    `json:"title,omitempty"`
	Error        string    `json:"error,omitempty"`
	LatencyMs    int64     `json:"latency_ms"`
	TestedAt     time.Time `json:"tested_at"`
}

// KeyStats is the persisted usage counter of one API key.
type KeyStats struct {
	Requests   int64      `json:"requests"`
	Errors     int64      `json:"errors"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
}

// DB wraps the SQLite database.
type DB struct {
	sql *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS call_logs (
	id TEXT PRIMARY KEY,
	time TEXT NOT NULL,
	protocol TEXT NOT NULL DEFAULT '',
	api_key_id TEXT NOT NULL DEFAULT '',
	api_key_name TEXT NOT NULL DEFAULT '',
	api_key_prefix TEXT NOT NULL DEFAULT '',
	route_id TEXT NOT NULL DEFAULT '',
	route_name TEXT NOT NULL DEFAULT '',
	mode TEXT NOT NULL DEFAULT '',
	channels TEXT NOT NULL DEFAULT '[]',
	query TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	result_count INTEGER NOT NULL DEFAULT 0,
	status INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	remote_addr TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	request_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_call_logs_time ON call_logs(time);
CREATE INDEX IF NOT EXISTS idx_call_logs_protocol ON call_logs(protocol);
CREATE INDEX IF NOT EXISTS idx_call_logs_status ON call_logs(status);
CREATE INDEX IF NOT EXISTS idx_call_logs_key ON call_logs(api_key_id);
CREATE INDEX IF NOT EXISTS idx_call_logs_route ON call_logs(route_id);

CREATE TABLE IF NOT EXISTS channel_stats (
	channel_id TEXT PRIMARY KEY,
	search_requests INTEGER NOT NULL DEFAULT 0,
	search_errors INTEGER NOT NULL DEFAULT 0,
	fetch_requests INTEGER NOT NULL DEFAULT 0,
	fetch_errors INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	last_used_at TEXT
);

CREATE TABLE IF NOT EXISTS channel_tests (
	channel_id TEXT NOT NULL,
	role TEXT NOT NULL,
	ok INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL DEFAULT '',
	query TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	result_count INTEGER NOT NULL DEFAULT 0,
	content_chars INTEGER NOT NULL DEFAULT 0,
	title TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	latency_ms INTEGER NOT NULL DEFAULT 0,
	tested_at TEXT NOT NULL,
	PRIMARY KEY (channel_id, role)
);

CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS key_stats (
	key_id TEXT PRIMARY KEY,
	requests INTEGER NOT NULL DEFAULT 0,
	errors INTEGER NOT NULL DEFAULT 0,
	last_used_at TEXT,
	last_error TEXT NOT NULL DEFAULT ''
);
`

// Open opens (and creates/migrates) the SQLite database.
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite handles one writer; a small pool avoids SQLITE_BUSY under load.
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("init sqlite schema: %w", err)
	}
	// The database contains call logs and query text, so keep it private.
	if err := os.Chmod(path, 0o600); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("chmod sqlite %s: %w", path, err)
	}
	return &DB{sql: sqlDB}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// CloseIdle releases idle connections (used by tests).
func (d *DB) CloseIdle() { d.sql.SetMaxIdleConns(0) }

// timeLayout keeps a fixed number of fractional digits so that ORDER BY time
// gives the same order as the actual timestamps.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTimePtr(raw sql.NullString) *time.Time {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil
	}
	return &t
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

const insertLogSQL = `INSERT OR IGNORE INTO call_logs (
	id, time, protocol, api_key_id, api_key_name, api_key_prefix,
	route_id, route_name, mode, channels, query, url, result_count,
	status, error, duration_ms, remote_addr, user_agent, request_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func logArgs(entry logstore.Entry) []any {
	if entry.ID == "" {
		entry.ID = newID("log")
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	channels, _ := json.Marshal(entry.Channels)
	return []any{
		entry.ID, formatTime(entry.Time), entry.Protocol,
		entry.APIKeyID, entry.APIKeyName, entry.APIKeyPrefix,
		entry.RouteID, entry.RouteName, entry.Mode, string(channels),
		entry.Query, entry.URL, entry.ResultCount,
		entry.Status, entry.Error, entry.DurationMs,
		entry.RemoteAddr, entry.UserAgent, entry.RequestID,
	}
}

// InsertLog appends one call-log row.
func (d *DB) InsertLog(entry logstore.Entry) error {
	_, err := d.sql.Exec(insertLogSQL, logArgs(entry)...)
	if err != nil {
		return fmt.Errorf("insert call log: %w", err)
	}
	return nil
}

// ImportJSONL imports the legacy JSONL call log when the table is empty and the
// file exists. It returns the number of imported rows.
func (d *DB) ImportJSONL(path string) (int, error) {
	var marker string
	if err := d.sql.QueryRow(`SELECT value FROM meta WHERE key = 'legacy_log_imported'`).Scan(&marker); err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if marker == "1" {
		return 0, nil
	}
	var count int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM call_logs`).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 || strings.TrimSpace(path) == "" {
		if err := d.markLegacyImported(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("open legacy log %s: %w", path, err)
	}
	defer file.Close()
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	imported := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry logstore.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if _, err := tx.Exec(insertLogSQL, logArgs(entry)...); err != nil {
			tx.Rollback()
			return imported, fmt.Errorf("import call log: %w", err)
		}
		imported++
	}
	if err := scanner.Err(); err != nil {
		tx.Rollback()
		return imported, fmt.Errorf("read legacy log %s: %w", path, err)
	}
	if err := tx.Commit(); err != nil {
		return imported, err
	}
	if err := d.markLegacyImported(); err != nil {
		return imported, err
	}
	return imported, nil
}

func (d *DB) markLegacyImported() error {
	_, err := d.sql.Exec(`INSERT INTO meta (key, value) VALUES ('legacy_log_imported', '1') ON CONFLICT(key) DO UPDATE SET value = '1'`)
	return err
}

// CountLogs returns the total number of call-log rows.
func (d *DB) CountLogs() (int, error) {
	var count int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM call_logs`).Scan(&count)
	return count, err
}

// ListLogs returns one page of call logs (newest first) plus the total count.
func (d *DB) ListLogs(filter logstore.Filter) ([]logstore.Entry, int, error) {
	f := filter.Normalize()
	where, args := buildLogWhere(f)
	var total int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM call_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count call logs: %w", err)
	}
	query := `SELECT id, time, protocol, api_key_id, api_key_name, api_key_prefix,
		route_id, route_name, mode, channels, query, url, result_count,
		status, error, duration_ms, remote_addr, user_agent, request_id
		FROM call_logs` + where + ` ORDER BY time DESC, rowid DESC LIMIT ? OFFSET ?`
	rows, err := d.sql.Query(query, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query call logs: %w", err)
	}
	defer rows.Close()
	entries := make([]logstore.Entry, 0, f.Limit)
	for rows.Next() {
		var entry logstore.Entry
		var rawTime, channels string
		if err := rows.Scan(
			&entry.ID, &rawTime, &entry.Protocol,
			&entry.APIKeyID, &entry.APIKeyName, &entry.APIKeyPrefix,
			&entry.RouteID, &entry.RouteName, &entry.Mode, &channels,
			&entry.Query, &entry.URL, &entry.ResultCount,
			&entry.Status, &entry.Error, &entry.DurationMs,
			&entry.RemoteAddr, &entry.UserAgent, &entry.RequestID,
		); err != nil {
			return nil, 0, err
		}
		if t, err := time.Parse(time.RFC3339Nano, rawTime); err == nil {
			entry.Time = t
		}
		_ = json.Unmarshal([]byte(channels), &entry.Channels)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

func buildLogWhere(f logstore.Filter) (string, []any) {
	var clauses []string
	var args []any
	if f.Protocol != "" {
		clauses = append(clauses, "protocol = ?")
		args = append(args, f.Protocol)
	}
	switch strings.ToLower(f.Status) {
	case "", "all":
	case "ok":
		clauses = append(clauses, "status BETWEEN 200 AND 299")
	case "error":
		clauses = append(clauses, "(status < 200 OR status >= 300)")
	default:
		if status, err := strconv.Atoi(f.Status); err == nil {
			clauses = append(clauses, "status = ?")
			args = append(args, status)
		}
	}
	if f.KeyID != "" {
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, f.KeyID)
	}
	if f.RouteID != "" {
		clauses = append(clauses, "route_id = ?")
		args = append(args, f.RouteID)
	}
	if f.Channel != "" {
		clauses = append(clauses, "channels LIKE ?")
		args = append(args, `%"`+f.Channel+`"%`)
	}
	if f.Search != "" {
		clauses = append(clauses, "(query LIKE ? OR url LIKE ? OR error LIKE ?)")
		like := "%" + f.Search + "%"
		args = append(args, like, like, like)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "time >= ?")
		args = append(args, formatTime(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "time <= ?")
		args = append(args, formatTime(f.Until))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// ClearLogs deletes every call log.
func (d *DB) ClearLogs() error {
	_, err := d.sql.Exec(`DELETE FROM call_logs`)
	return err
}

// RecordChannelAttempt bumps channel usage counters. A non-nil error increments
// the error counter and stores the message.
func (d *DB) RecordChannelAttempt(channelID, role string, callErr error) error {
	if channelID == "" || (role != "search" && role != "fetch") {
		return nil
	}
	var searchReq, searchErr, fetchReq, fetchErr int
	if role == "search" {
		if callErr == nil {
			searchReq = 1
		} else {
			searchErr = 1
		}
	} else {
		if callErr == nil {
			fetchReq = 1
		} else {
			fetchErr = 1
		}
	}
	errText := ""
	if callErr != nil {
		errText = callErr.Error()
	}
	_, err := d.sql.Exec(`
		INSERT INTO channel_stats (channel_id, search_requests, search_errors, fetch_requests, fetch_errors, last_error, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id) DO UPDATE SET
			search_requests = channel_stats.search_requests + excluded.search_requests,
			search_errors = channel_stats.search_errors + excluded.search_errors,
			fetch_requests = channel_stats.fetch_requests + excluded.fetch_requests,
			fetch_errors = channel_stats.fetch_errors + excluded.fetch_errors,
			last_error = CASE WHEN excluded.last_error <> '' THEN excluded.last_error ELSE channel_stats.last_error END,
			last_used_at = excluded.last_used_at`,
		channelID, searchReq, searchErr, fetchReq, fetchErr, errText, formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("record channel attempt: %w", err)
	}
	return nil
}

// ChannelStats returns all channel usage counters.
func (d *DB) ChannelStats() (map[string]ChannelStats, error) {
	rows, err := d.sql.Query(`SELECT channel_id, search_requests, search_errors, fetch_requests, fetch_errors, last_error, last_used_at FROM channel_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ChannelStats{}
	for rows.Next() {
		var id string
		var stat ChannelStats
		var lastUsed sql.NullString
		if err := rows.Scan(&id, &stat.SearchRequests, &stat.SearchErrors, &stat.FetchRequests, &stat.FetchErrors, &stat.LastError, &lastUsed); err != nil {
			return nil, err
		}
		stat.LastUsedAt = parseTimePtr(lastUsed)
		out[id] = stat
	}
	return out, rows.Err()
}

// RecordChannelTest upserts the last connectivity test of a channel.
func (d *DB) RecordChannelTest(channelID, role string, test ChannelTest) error {
	if channelID == "" || (role != "search" && role != "fetch") {
		return nil
	}
	_, err := d.sql.Exec(`
		INSERT INTO channel_tests (channel_id, role, ok, kind, query, url, result_count, content_chars, title, error, latency_ms, tested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(channel_id, role) DO UPDATE SET
			ok = excluded.ok, kind = excluded.kind, query = excluded.query, url = excluded.url,
			result_count = excluded.result_count, content_chars = excluded.content_chars,
			title = excluded.title, error = excluded.error, latency_ms = excluded.latency_ms, tested_at = excluded.tested_at`,
		channelID, role, boolInt(test.OK), test.Kind, test.Query, test.URL, test.ResultCount,
		test.ContentChars, test.Title, test.Error, test.LatencyMs, formatTime(test.TestedAt))
	if err != nil {
		return fmt.Errorf("record channel test: %w", err)
	}
	return nil
}

// ChannelTests returns all persisted channel test results.
func (d *DB) ChannelTests() (map[string]map[string]ChannelTest, error) {
	rows, err := d.sql.Query(`SELECT channel_id, role, ok, kind, query, url, result_count, content_chars, title, error, latency_ms, tested_at FROM channel_tests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]ChannelTest{}
	for rows.Next() {
		var channelID, role, testedAt string
		var test ChannelTest
		var ok int
		if err := rows.Scan(&channelID, &role, &ok, &test.Kind, &test.Query, &test.URL, &test.ResultCount, &test.ContentChars, &test.Title, &test.Error, &test.LatencyMs, &testedAt); err != nil {
			return nil, err
		}
		test.OK = ok != 0
		if t, err := time.Parse(time.RFC3339Nano, testedAt); err == nil {
			test.TestedAt = t
		}
		if out[channelID] == nil {
			out[channelID] = map[string]ChannelTest{}
		}
		out[channelID][role] = test
	}
	return out, rows.Err()
}

// RecordKeyAttempt bumps API key usage counters.
func (d *DB) RecordKeyAttempt(keyID string, callErr error) error {
	if keyID == "" {
		return nil
	}
	req, errCount := 1, 0
	if callErr != nil {
		req, errCount = 0, 1
	}
	errText := ""
	if callErr != nil {
		errText = callErr.Error()
	}
	_, err := d.sql.Exec(`
		INSERT INTO key_stats (key_id, requests, errors, last_used_at, last_error)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key_id) DO UPDATE SET
			requests = key_stats.requests + excluded.requests,
			errors = key_stats.errors + excluded.errors,
			last_used_at = excluded.last_used_at,
			last_error = CASE WHEN excluded.last_error <> '' THEN excluded.last_error ELSE key_stats.last_error END`,
		keyID, req, errCount, formatTime(time.Now()), errText)
	if err != nil {
		return fmt.Errorf("record key attempt: %w", err)
	}
	return nil
}

// KeyStats returns all API key usage counters.
func (d *DB) KeyStats() (map[string]KeyStats, error) {
	rows, err := d.sql.Query(`SELECT key_id, requests, errors, last_used_at, last_error FROM key_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]KeyStats{}
	for rows.Next() {
		var id string
		var stat KeyStats
		var lastUsed sql.NullString
		if err := rows.Scan(&id, &stat.Requests, &stat.Errors, &lastUsed, &stat.LastError); err != nil {
			return nil, err
		}
		stat.LastUsedAt = parseTimePtr(lastUsed)
		out[id] = stat
	}
	return out, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
