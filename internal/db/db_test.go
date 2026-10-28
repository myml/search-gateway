package db

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"search-gateway/internal/logstore"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestLogsInsertListFilterClear(t *testing.T) {
	database := newTestDB(t)
	base := time.Now().UTC().Add(-time.Minute)
	entries := []logstore.Entry{
		{ID: "log_1", Time: base, Protocol: "search", APIKeyID: "key_1", RouteID: "rt_1", RouteName: "default", Channels: []string{"sx-a"}, Query: "alpha", Status: 200, DurationMs: 10},
		{ID: "log_2", Time: base.Add(time.Second), Protocol: "fetch", APIKeyID: "key_1", RouteID: "rt_1", Channels: []string{"direct"}, URL: "https://example.com", Status: 502, Error: "boom", DurationMs: 20},
		{ID: "log_3", Time: base.Add(2 * time.Second), Protocol: "auth", APIKeyPrefix: "sg_abc", Status: 401, Error: "invalid api key"},
	}
	for _, entry := range entries {
		if err := database.InsertLog(entry); err != nil {
			t.Fatal(err)
		}
	}
	total, err := database.CountLogs()
	if err != nil || total != 3 {
		t.Fatalf("count = %d, %v", total, err)
	}

	got, total, err := database.ListLogs(logstore.Filter{})
	if err != nil || total != 3 || len(got) != 3 {
		t.Fatalf("list = %d/%d, %v", len(got), total, err)
	}
	if got[0].ID != "log_3" {
		t.Fatalf("newest first = %+v", got[0])
	}
	if got[2].ID != "log_1" || len(got[2].Channels) != 1 || got[2].Channels[0] != "sx-a" {
		t.Fatalf("channels round trip = %+v", got[2])
	}

	if _, total, _ := database.ListLogs(logstore.Filter{Protocol: "search"}); total != 1 {
		t.Fatalf("protocol filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Status: "error"}); total != 2 {
		t.Fatalf("error filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Status: "ok"}); total != 1 {
		t.Fatalf("ok filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Status: "401"}); total != 1 {
		t.Fatalf("status filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{KeyID: "key_1"}); total != 2 {
		t.Fatalf("key filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{RouteID: "rt_1"}); total != 2 {
		t.Fatalf("route filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Channel: "direct"}); total != 1 {
		t.Fatalf("channel filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Search: "boom"}); total != 1 {
		t.Fatalf("search filter = %d", total)
	}
	if _, total, _ := database.ListLogs(logstore.Filter{Since: base.Add(1500 * time.Millisecond)}); total != 1 {
		t.Fatalf("since filter = %d", total)
	}
	if got, _, _ := database.ListLogs(logstore.Filter{Limit: 1, Offset: 1}); len(got) != 1 || got[0].ID != "log_2" {
		t.Fatalf("paging = %+v", got)
	}

	if err := database.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	if got, total, _ := database.ListLogs(logstore.Filter{}); len(got) != 0 || total != 0 {
		t.Fatalf("clear = %+v/%d", got, total)
	}
}

func TestImportJSONL(t *testing.T) {
	database := newTestDB(t)
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	content := `{"id":"log_a","time":"2026-01-02T03:04:05Z","protocol":"search","query":"q1","status":200}
{"id":"log_b","time":"2026-01-02T03:04:06Z","protocol":"fetch","url":"https://x","status":502,"error":"bad"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, err := database.ImportJSONL(path)
	if err != nil || imported != 2 {
		t.Fatalf("import = %d, %v", imported, err)
	}
	entries, total, err := database.ListLogs(logstore.Filter{})
	if err != nil || total != 2 || entries[0].ID != "log_b" {
		t.Fatalf("imported logs = %+v, %v", entries, err)
	}
	if again, _ := database.ImportJSONL(path); again != 0 {
		t.Fatalf("second import = %d", again)
	}
	// Clearing logs must not resurrect the legacy JSONL on the next import.
	if err := database.ClearLogs(); err != nil {
		t.Fatal(err)
	}
	if again, _ := database.ImportJSONL(path); again != 0 {
		t.Fatalf("reimport after clear = %d", again)
	}
}

func TestStatsTestsAndKeyUsage(t *testing.T) {
	database := newTestDB(t)
	now := time.Now().UTC()

	if err := database.RecordChannelAttempt("ch_1", "search", nil); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChannelAttempt("ch_1", "search", os.ErrDeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChannelAttempt("ch_1", "fetch", nil); err != nil {
		t.Fatal(err)
	}
	stats, err := database.ChannelStats()
	if err != nil {
		t.Fatal(err)
	}
	stat := stats["ch_1"]
	if stat.SearchRequests != 1 || stat.SearchErrors != 1 || stat.FetchRequests != 1 || stat.LastError == "" || stat.LastUsedAt == nil {
		t.Fatalf("channel stats = %+v", stat)
	}

	test := ChannelTest{OK: true, Kind: "search", Query: "q", ResultCount: 3, Title: "T", LatencyMs: 12, TestedAt: now}
	if err := database.RecordChannelTest("ch_1", "search", test); err != nil {
		t.Fatal(err)
	}
	test.OK = false
	test.Error = "failed"
	if err := database.RecordChannelTest("ch_1", "search", test); err != nil {
		t.Fatal(err)
	}
	tests, err := database.ChannelTests()
	if err != nil {
		t.Fatal(err)
	}
	if got := tests["ch_1"]["search"]; got.OK || got.Error != "failed" || got.ResultCount != 3 {
		t.Fatalf("channel test = %+v", got)
	}

	if err := database.RecordKeyAttempt("key_1", nil); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordKeyAttempt("key_1", os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	keyStats, err := database.KeyStats()
	if err != nil {
		t.Fatal(err)
	}
	if got := keyStats["key_1"]; got.Requests != 1 || got.Errors != 1 || got.LastError == "" || got.LastUsedAt == nil {
		t.Fatalf("key stats = %+v", got)
	}
}

func TestDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("db mode = %o", info.Mode().Perm())
	}
}

func TestChannelUsageAndQuota(t *testing.T) {
	database := newTestDB(t)
	const period = "2026-10"

	allowed, err := database.ConsumeChannelQuota("ch_1", period, 2)
	if err != nil || !allowed {
		t.Fatalf("first consume = %v, %v", allowed, err)
	}
	if allowed, err = database.ConsumeChannelQuota("ch_1", period, 2); err != nil || !allowed {
		t.Fatalf("second consume = %v, %v", allowed, err)
	}
	if allowed, err = database.ConsumeChannelQuota("ch_1", period, 2); err != nil || allowed {
		t.Fatalf("third consume = %v, %v", allowed, err)
	}
	usage, err := database.ChannelUsage(period)
	if err != nil {
		t.Fatal(err)
	}
	if got := usage["ch_1"]; got.Requests != 2 || got.Errors != 0 {
		t.Fatalf("usage = %+v", got)
	}

	// Unlimited channels keep counting calls.
	if allowed, err = database.ConsumeChannelQuota("ch_1", period, 0); err != nil || !allowed {
		t.Fatalf("unlimited consume = %v, %v", allowed, err)
	}
	if err := database.RecordChannelUsageError("ch_1", period); err != nil {
		t.Fatal(err)
	}
	if usage, err = database.ChannelUsage(period); err != nil {
		t.Fatal(err)
	}
	if got := usage["ch_1"]; got.Requests != 3 || got.Errors != 1 {
		t.Fatalf("usage after error = %+v", got)
	}

	// A new period starts from zero.
	if allowed, err = database.ConsumeChannelQuota("ch_1", "2026-11", 1); err != nil || !allowed {
		t.Fatalf("next period consume = %v, %v", allowed, err)
	}
	if allowed, err = database.ConsumeChannelQuota("ch_1", "2026-11", 1); err != nil || allowed {
		t.Fatalf("next period second consume = %v, %v", allowed, err)
	}

	if err := database.ResetChannelUsage(period, "ch_1"); err != nil {
		t.Fatal(err)
	}
	if usage, err = database.ChannelUsage(period); err != nil {
		t.Fatal(err)
	}
	if len(usage) != 0 {
		t.Fatalf("usage after reset = %+v", usage)
	}
	if usage, err = database.ChannelUsage("2026-11"); err != nil || usage["ch_1"].Requests != 1 {
		t.Fatalf("other period usage = %+v, %v", usage, err)
	}
}

func TestChannelQuotaIsAtomicUnderConcurrency(t *testing.T) {
	database := newTestDB(t)
	const period = "2026-10"
	const limit = 10
	const attempts = 40

	results := make(chan bool, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, err := database.ConsumeChannelQuota("ch_1", period, limit)
			if err != nil {
				t.Errorf("consume: %v", err)
				return
			}
			results <- allowed
		}()
	}
	wg.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	if allowedCount != limit {
		t.Fatalf("allowed = %d, want %d", allowedCount, limit)
	}
	usage, err := database.ChannelUsage(period)
	if err != nil {
		t.Fatal(err)
	}
	if got := usage["ch_1"].Requests; got != limit {
		t.Fatalf("requests = %d, want %d", got, limit)
	}
}

func TestResetChannelUsageForWholePeriodAndDelete(t *testing.T) {
	database := newTestDB(t)
	const period = "2026-10"
	for _, id := range []string{"ch_1", "ch_2"} {
		if _, err := database.ConsumeChannelQuota(id, period, 5); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.ResetChannelUsage(period, ""); err != nil {
		t.Fatal(err)
	}
	if usage, err := database.ChannelUsage(period); err != nil || len(usage) != 0 {
		t.Fatalf("usage after period reset = %+v, %v", usage, err)
	}

	if _, err := database.ConsumeChannelQuota("ch_1", period, 5); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteChannelUsage("ch_1"); err != nil {
		t.Fatal(err)
	}
	if usage, err := database.ChannelUsage(period); err != nil || len(usage) != 0 {
		t.Fatalf("usage after delete = %+v, %v", usage, err)
	}
}
