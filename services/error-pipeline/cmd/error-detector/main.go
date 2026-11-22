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
	"nacfson_pipeline/services/error-pipeline/internal/kafka"
	"nacfson_pipeline/services/error-pipeline/internal/kafkaclient"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

const (
	defaultListenAddr        = ":8080"
	defaultVLogsURL          = "http://victorialogs.observability.svc:9428"
	defaultNotifyURL         = "http://notify-gateway.vault.svc:8080"
	defaultInvestigator      = ""
	defaultStatePath         = "/state/detector.json"
	defaultSourceMode        = "vlogs"
	defaultNotifyMode        = "http"
	defaultKafkaBrokers      = "10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092"
	defaultKafkaTLSDir       = "/etc/error-detector/tls"
	defaultOutboxTopic       = "alerts.errors"
	defaultDedupTTL          = 10 * time.Minute
	defaultQuarantineWait    = time.Hour
	defaultDisklessMaxGroups = 10000
	probeReadHeaderTime      = 5 * time.Second
	probeWriteTimeout        = 30 * time.Second
	shutdownGrace            = 30 * time.Second
)

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no":
		return false
	case "true", "1", "yes":
		return true
	default:
		return def
	}
}

func envDuration(key string, def time.Duration, logger *slog.Logger) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		logger.Error("invalid duration environment variable", "key", key, "value", raw)
		os.Exit(1)
	}
	return d
}

func kafkaConfigFromEnv() kafkaclient.Config {
	tlsDir := envOrDefault("KAFKA_TLS_DIR", defaultKafkaTLSDir)
	rawBrokers := strings.Split(envOrDefault("KAFKA_BROKERS", defaultKafkaBrokers), ",")
	brokers := make([]string, 0, len(rawBrokers))
	for _, b := range rawBrokers {
		if trimmed := strings.TrimSpace(b); trimmed != "" {
			brokers = append(brokers, trimmed)
		}
	}
	return kafkaclient.Config{
		Brokers:  brokers,
		CAFile:   envOrDefault("KAFKA_CA_FILE", filepath.Join(tlsDir, "ca.crt")),
		CertFile: envOrDefault("KAFKA_CERT_FILE", filepath.Join(tlsDir, "tls.crt")),
		KeyFile:  envOrDefault("KAFKA_KEY_FILE", filepath.Join(tlsDir, "tls.key")),
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	listenAddr := envOrDefault("LISTEN_ADDR", defaultListenAddr)
	sourceMode := strings.ToLower(envOrDefault("SOURCE_MODE", defaultSourceMode))
	notifyMode := strings.ToLower(envOrDefault("NOTIFY_MODE", defaultNotifyMode))
	stateEnabled := envBool("STATE_ENABLED", true)
	investigatorURL := envOrDefault("INVESTIGATOR_URL", defaultInvestigator)
	grafanaBaseURL := envOrDefault("GRAFANA_BASE_URL", "")
	quarantineThrottle := envDuration("QUARANTINE_THROTTLE", defaultQuarantineWait, logger)

	statePath := ""
	if stateEnabled {
		statePath = envOrDefault("STATE_PATH", defaultStatePath)
		if dir := filepath.Dir(statePath); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				logger.Error("cannot create the state directory", "path", dir, "error", err.Error())
				os.Exit(1)
			}
		}
	}

	groupCfg := group.DefaultConfig()
	_, dedupSet := os.LookupEnv("DEDUP_TTL")
	if sourceMode == "kafka" || !stateEnabled || dedupSet {
		ttl := envDuration("DEDUP_TTL", defaultDedupTTL, logger)
		groupCfg.Cooldown = ttl
		if investigatorURL == "" {
			groupCfg.Window = 0
		}
		groupCfg.MaxEntries = defaultDisklessMaxGroups
		if !stateEnabled {
			groupCfg.Retention = 2 * ttl
		}
	}

	cfg := detector.Config{
		InvestigatorURL:    investigatorURL,
		StatePath:          statePath,
		StateDisabled:      !stateEnabled,
		QuarantineThrottle: quarantineThrottle,
		GrafanaBaseURL:     grafanaBaseURL,
		Group:              groupCfg,
		Now:                time.Now,
	}

	var closers []func()
	defer func() {
		for _, fn := range closers {
			fn()
		}
	}()

	switch sourceMode {
	case "vlogs":
		cfg.VLogs = &vlogs.Client{
			BaseURL: strings.TrimRight(envOrDefault("VLOGS_URL", defaultVLogsURL), "/"),
			HTTP:    &http.Client{},
		}
	case "kafka":
		src, err := kafka.NewSource(kafka.Config{
			Client:          kafkaConfigFromEnv(),
			Group:           envOrDefault("KAFKA_GROUP", kafka.DefaultGroup),
			ValidTopic:      envOrDefault("KAFKA_VALID_TOPIC", kafka.DefaultValidTopic),
			QuarantineTopic: envOrDefault("KAFKA_QUARANTINE_TOPIC", kafka.DefaultQuarantineTopic),
		})
		if err != nil {
			logger.Error("cannot create kafka source", "error", err.Error())
			os.Exit(1)
		}
		closers = append(closers, src.Close)
		cfg.Source = src
	default:
		logger.Error("unsupported SOURCE_MODE", "mode", sourceMode)
		os.Exit(1)
	}

	switch notifyMode {
	case "http":
		cfg.Notify = &notify.Client{
			BaseURL: strings.TrimRight(envOrDefault("NOTIFY_URL", defaultNotifyURL), "/"),
		}
	case "kafka-outbox":
		outbox, err := notify.NewKafkaOutbox(kafkaConfigFromEnv(), envOrDefault("KAFKA_OUTBOX_TOPIC", defaultOutboxTopic))
		if err != nil {
			logger.Error("cannot create kafka outbox", "error", err.Error())
			os.Exit(1)
		}
		closers = append(closers, outbox.Close)
		cfg.Notify = outbox
		cfg.SyncNotify = true
	default:
		logger.Error("unsupported NOTIFY_MODE", "mode", notifyMode)
		os.Exit(1)
	}

	d, err := detector.New(cfg)
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
		logger.Info("probe listener starting", "addr", listenAddr, "source_mode", sourceMode, "notify_mode", notifyMode, "state_enabled", stateEnabled)
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
