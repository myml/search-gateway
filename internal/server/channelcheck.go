package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"search-gateway/internal/db"
	"search-gateway/internal/logstore"
	"search-gateway/internal/search"
	"search-gateway/internal/store"
)

// ChannelCheck is the outcome of the most recent connectivity test for a channel.
type ChannelCheck = db.ChannelTest

func summarizeCheck(query, rawURL string, result any, latency time.Duration, callErr error) ChannelCheck {
	check := ChannelCheck{
		OK:        callErr == nil,
		Kind:      "search",
		Query:     query,
		URL:       rawURL,
		LatencyMs: latency.Milliseconds(),
		TestedAt:  time.Now().UTC(),
	}
	if query == "" {
		check.Kind = "fetch"
	}
	if callErr != nil {
		check.Error = callErr.Error()
		return check
	}
	switch value := result.(type) {
	case []search.Result:
		check.ResultCount = len(value)
		if len(value) > 0 {
			check.Title = value[0].Title
		}
	case search.FetchResult:
		applyFetchCheck(&check, value)
	case *search.FetchResult:
		if value != nil {
			applyFetchCheck(&check, *value)
		}
	}
	return check
}

func applyFetchCheck(check *ChannelCheck, result search.FetchResult) {
	check.Title = result.Title
	check.ContentChars = len([]rune(result.Content))
}

func (s *Server) recordChannelCheck(id, role string, check ChannelCheck) {
	if err := s.db.RecordChannelTest(id, role, check); err != nil {
		s.log.Warn("record channel test failed", "channel", id, "role", role, "err", err)
	}
}

func (s *Server) channelChecks(id string) map[string]ChannelCheck {
	checks, err := s.db.ChannelTests()
	if err != nil {
		s.log.Warn("load channel tests failed", "err", err)
		return map[string]ChannelCheck{}
	}
	return checks[id]
}

// handleAdminTestChannels tests every channel (or the selected ones) concurrently.
func (s *Server) handleAdminTestChannels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role       string   `json:"role"`
		Query      string   `json:"query"`
		URL        string   `json:"url"`
		ChannelIDs []string `json:"channel_ids"`
	}
	if err := decodeJSONBody(w, r, &in); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	role := strings.TrimSpace(in.Role)
	if role != "" && role != "search" && role != "fetch" {
		writeError(w, http.StatusBadRequest, "role must be search or fetch")
		return
	}
	snapshot := s.store.Snapshot()
	selected := make(map[string]bool, len(in.ChannelIDs))
	for _, id := range in.ChannelIDs {
		selected[id] = true
	}
	channels := make([]store.Channel, 0, len(snapshot.Channels))
	for _, ch := range snapshot.Channels {
		if len(selected) > 0 && !selected[ch.ID] {
			continue
		}
		if role != "" && !ch.Supports(role) {
			continue
		}
		channels = append(channels, ch)
	}
	if len(channels) == 0 {
		writeError(w, http.StatusBadRequest, "no channels to test")
		return
	}

	type testResult struct {
		ChannelID string       `json:"channel_id"`
		Name      string       `json:"name"`
		Enabled   bool         `json:"enabled"`
		Check     ChannelCheck `json:"check"`
	}
	results := make([]testResult, len(channels))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range channels {
		wg.Add(1)
		go func(i int, ch store.Channel) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			query := strings.TrimSpace(in.Query)
			rawURL := strings.TrimSpace(in.URL)
			switch role {
			case "search":
				rawURL = ""
				if query == "" {
					query = "search-gateway test"
				}
			case "fetch":
				query = ""
				if rawURL == "" {
					rawURL = "https://example.com/"
				}
			default:
				if ch.Supports("search") {
					if query == "" {
						query = "search-gateway test"
					}
					rawURL = ""
				} else {
					query = ""
					if rawURL == "" {
						rawURL = "https://example.com/"
					}
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			start := time.Now()
			value, err := s.engine.TestChannel(ctx, ch, query, rawURL)
			check := summarizeCheck(query, rawURL, value, time.Since(start), err)
			if role != "" {
				check.Kind = role
			}
			s.recordChannelCheck(ch.ID, check.Kind, check)

			entry := logstore.Entry{
				Protocol:   "admin-test",
				Time:       start.UTC(),
				Query:      query,
				URL:        rawURL,
				Status:     http.StatusOK,
				Channels:   []string{ch.Name},
				RouteName:  "admin-test",
				DurationMs: check.LatencyMs,
				RemoteAddr: r.RemoteAddr,
				UserAgent:  r.UserAgent(),
				RequestID:  requestIDFrom(r),
			}
			if err != nil {
				entry.Status = http.StatusBadGateway
				entry.Error = err.Error()
			}
			if appendErr := s.db.InsertLog(entry); appendErr != nil {
				s.log.Warn("append admin-test log failed", "err", appendErr)
			}
			results[i] = testResult{ChannelID: ch.ID, Name: ch.Name, Enabled: ch.Enabled, Check: check}
		}(i, channels[i])
	}
	wg.Wait()

	okCount, failCount := 0, 0
	for _, result := range results {
		if result.Check.OK {
			okCount++
		} else {
			failCount++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":    results,
		"ok_count":   okCount,
		"fail_count": failCount,
	})
}
