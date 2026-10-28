package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"search-gateway/internal/routing"
	"search-gateway/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError renders the OpenAI/DeepSeek-compatible error envelope.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "api_error",
			"param":   nil,
			"code":    nil,
		},
	})
}

// writeAnthropicError renders the Anthropic Messages error envelope.
func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

func gatewayStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, routing.ErrNoChannel):
		return http.StatusServiceUnavailable
	case errors.Is(err, routing.ErrQuotaExhausted):
		return http.StatusServiceUnavailable
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrUnauthorized):
		return http.StatusUnauthorized
	default:
		return http.StatusBadGateway
	}
}

func (s *Server) writeGatewayError(w http.ResponseWriter, err error) {
	writeError(w, gatewayStatus(err), err.Error())
}

func (s *Server) writeAnthropicGatewayError(w http.ResponseWriter, err error) {
	status := gatewayStatus(err)
	errType := "api_error"
	if status == http.StatusBadRequest {
		errType = "invalid_request_error"
	}
	writeAnthropicError(w, status, errType, err.Error())
}

func randomID(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(buf)
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrInvalid) {
		writeStoreError(w, err)
		return
	}
	s.log.Error("store operation failed", "err", err)
	writeStoreError(w, err)
}
