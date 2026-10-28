// Command search-gateway runs the search/fetch gateway and its console.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"search-gateway/internal/config"
	"search-gateway/internal/db"
	"search-gateway/internal/routing"
	"search-gateway/internal/server"
	"search-gateway/internal/store"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	listen := flag.String("listen", "", "override the configured listen address")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	st, err := store.Open(cfg.StorePath)
	if err != nil {
		logger.Error("open store failed", "err", err)
		os.Exit(1)
	}
	if err := st.SeedFromConfig(cfg); err != nil {
		logger.Error("seed store from config failed", "err", err)
		os.Exit(1)
	}
	snapshot := st.Snapshot()
	if len(snapshot.APIKeys) == 0 {
		logger.Warn("no API keys configured: open the console with an admin token and create one")
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Error("open database failed", "path", cfg.DBPath, "err", err)
		os.Exit(1)
	}
	defer database.Close()
	if imported, importErr := database.ImportJSONL(cfg.LogPath); importErr != nil {
		logger.Warn("import legacy call log failed", "path", cfg.LogPath, "err", importErr)
	} else if imported > 0 {
		logger.Info("imported legacy call log", "path", cfg.LogPath, "rows", imported)
	}
	logCount, _ := database.CountLogs()

	engine := routing.New(st, cfg, database, logger)
	app := server.New(cfg, st, engine, database, logger)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		WriteTimeout:      120 * time.Second,
	}

	go func() {
		logger.Info("listening",
			"addr", cfg.Listen,
			"store", cfg.StorePath,
			"db", cfg.DBPath,
			"log_rows", logCount,
			"channels", len(snapshot.Channels),
			"routes", len(snapshot.Routes),
			"api_keys", len(snapshot.APIKeys),
		)
		if isLoopbackListen(cfg.Listen) {
			logger.Info("bound to loopback only; pass -listen 0.0.0.0:8787 to allow LAN access")
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	if err := database.Close(); err != nil {
		logger.Warn("close database failed", "err", err)
	}
	logger.Info("stopped")
}

// isLoopbackListen reports whether the listen address only accepts local clients.
func isLoopbackListen(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
