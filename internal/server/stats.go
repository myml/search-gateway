package server

import (
	"search-gateway/internal/db"
	"search-gateway/internal/store"
)

func (s *Server) recordKeyError(id string, err error) {
	if id == "" {
		return
	}
	if dbErr := s.db.RecordKeyAttempt(id, err); dbErr != nil {
		s.log.Warn("record key error failed", "key", id, "err", dbErr)
	}
}

func (s *Server) keyStatsSnapshot() map[string]KeyStats {
	stored, err := s.db.KeyStats()
	if err != nil {
		s.log.Warn("load key stats failed", "err", err)
		return map[string]KeyStats{}
	}
	out := make(map[string]KeyStats, len(stored))
	for id, stat := range stored {
		out[id] = db.KeyStats(stat)
	}
	return out
}

func keyStatFor(stats map[string]KeyStats, key store.APIKey) KeyStats {
	if stat, ok := stats[key.ID]; ok {
		return stat
	}
	return KeyStats{}
}
