package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/detector"
	"nacfson_pipeline/services/error-pipeline/internal/group"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

const (
	defaultListenAddr   = ":8080"
	defaultVLogsURL     = "http://victorialogs.observability.svc:9428"
	defaultNotifyURL    = "http://notify-gateway.vault.svc:8080"
	defaultInvestigator = ""
	defaultStatePath    = "/state/detector.json"
	probeReadHeaderTime = 5 * time.Second
	probeWriteTimeout   = 30 * time.Second
	shutdownGrace       = 30 * time.Second
)

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	listenAddr := envOrDefault("LISTEN_ADDR", defaultListenAddr)
	statePath := envOrDefault("STATE_PATH", defaultStatePath)

	vlogsClient := &vlogs.Client{
		BaseURL: strings.TrimRight(envOrDefault("VLOGS_URL", defaultVLogsURL), "/"),
		HTTP:    &http.Client{},
	}

	notifyClient := &notify.Client{
		BaseURL: strings.TrimRight(envOrDefault("NOTIFY_URL", defaultNotifyURL), "/"),
	}

	if dir := filepath.Dir(statePath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Error("cannot create the state directory", "path", dir, "error", err.Error())
			os.Exit(1)
		}
	}

	d, err := detector.New(detector.Config{
		VLogs:           vlogsClient,
		Notify:          notifyClient,
		InvestigatorURL: envOrDefault("INVESTIGATOR_URL", defaultInvestigator),
		StatePath:       statePath,
		Group:           group.DefaultConfig(),
		Now:             time.Now,
	})
	if err != nil {
		logger.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	runDone := make(chan error, 1)
	go func() { runDone <- d.Run(ctx) }()

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           d.Mux(),
		ReadHeaderTimeout: probeReadHeaderTime,
		WriteTimeout:      probeWriteTimeout,
	}
	serveErr := make(chan error, 1)
	go func() {
		logger.Info("probe listener starting", "addr", listenAddr, "state", statePath)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:

		stop()
		<-runDone
		logger.Error("probe listener stopped", "error", err.Error())
		os.Exit(1)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("probe listener shutdown incomplete", "error", err.Error())
	}
	if err := <-runDone; err != nil {
		logger.Error("detector stopped with an error", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("stopped")
}
