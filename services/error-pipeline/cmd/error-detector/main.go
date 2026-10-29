// Command error-detector watches the proj-* logs in VictoriaLogs, alerts the
// #errors Discord channel for every new error through the vault-notify-proxy,
// and hands each error group to the error-investigator once its collection
// window closes.
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

// Environment defaults of the design (section 8.4 and the deployment manifest).
const (
	defaultListenAddr   = ":8080"
	defaultVLogsURL     = "http://victorialogs.observability.svc:9428"
	defaultNotifyURL    = "http://notify-gateway.vault.svc:8080"
	defaultInvestigator = "" // workstream 3 has not landed; empty disables the handoff
	defaultStatePath    = "/state/detector.json"
	probeReadHeaderTime = 5 * time.Second
	probeWriteTimeout   = 30 * time.Second // /readyz waits on VictoriaLogs for up to 10 s
	shutdownGrace       = 30 * time.Second // one final drain fits inside this
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

	// Tail streams indefinitely, so its client must have no Timeout: the
	// request lives as long as the process. CatchUp and Ready set their own
	// 30 s deadline internally.
	vlogsClient := &vlogs.Client{
		BaseURL: strings.TrimRight(envOrDefault("VLOGS_URL", defaultVLogsURL), "/"),
		HTTP:    &http.Client{},
	}
	// The notify queue bounds each send with its own context deadline, so this
	// client needs none; a client-wide one would also cut a slow retry short.
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

	// SIGTERM from Kubernetes and SIGINT from a terminal both mean "stop after
	// saving": Run flushes the open crashes, reports the burst overflow, saves
	// the cursor and groups, and makes one bounded delivery attempt. Windows
	// still open stay open in the state, so the next start hands them off.
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
		// A listener that stops on its own is fatal: the probes are how the
		// kubelet decides whether to restart this pod.
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
