package server

import (
	_ "embed"
	"net/http"
)

//go:embed ui/index.html
var indexHTML []byte

//go:embed ui/app.js
var appJS []byte

//go:embed ui/app.css
var appCSS []byte

// Version is exposed by the health, info and admin endpoints.
const Version = "0.2.0"

func (s *Server) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleAppJS(w http.ResponseWriter, _ *http.Request) {
	serveAsset(w, "application/javascript; charset=utf-8", appJS)
}

func (s *Server) handleAppCSS(w http.ResponseWriter, _ *http.Request) {
	serveAsset(w, "text/css; charset=utf-8", appCSS)
}

// serveAsset writes one embedded console asset with conservative caching.
func serveAsset(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
