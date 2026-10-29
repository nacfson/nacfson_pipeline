package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"nacfson_pipeline/services/vault-proxies/internal/openbao"
	"nacfson_pipeline/services/vault-proxies/internal/podid"
)

// openBaoStore keeps Discord webhook URLs at kv/notify/discord/<channel>.
// It logs in per call: loads happen once per channel (the proxy caches the
// result) and saves only on an operator action, so the cost is negligible.
type openBaoStore struct {
	client *openbao.Client
}

func webhookPath(channel string) string { return "notify/discord/" + channel }

func (s *openBaoStore) LoadWebhook(ctx context.Context, channel string) (string, error) {
	token, err := s.client.Login(ctx)
	if err != nil {
		return "", err
	}
	data, err := s.client.ReadKV(ctx, token, webhookPath(channel))
	if err != nil {
		return "", err
	}
	webhook, _ := data["url"].(string)
	return webhook, nil
}

func (s *openBaoStore) SaveWebhook(ctx context.Context, channel, webhook string) error {
	token, err := s.client.Login(ctx)
	if err != nil {
		return err
	}
	return s.client.WriteKV(ctx, token, webhookPath(channel), map[string]any{"url": webhook})
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(logger *slog.Logger, msg string, args ...any) {
	logger.Error(msg, args...)
	os.Exit(1)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	grants, err := ParseGrants(os.Getenv("NOTIFY_GRANTS"))
	if err != nil {
		fatal(logger, "invalid NOTIFY_GRANTS", "error", err.Error())
	}

	resolver := podid.NewResolver(podid.InClusterAPIServer(), podid.InClusterHTTPClient(), podid.DefaultTokenPath, 60*time.Second)
	discordClient := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	proxy := NewNotifyProxy(NotifyProxyConfig{
		DataAddr:  envOrDefault("DATA_ADDR", "0.0.0.0:8080"),
		AdminAddr: envOrDefault("ADMIN_ADDR", "127.0.0.1:8081"),
		Grants:    grants,
		Resolve:   resolver.Resolve,
		Vault: &openBaoStore{client: &openbao.Client{
			Addr:    envOrDefault("OPENBAO_ADDR", "http://openbao.vault.svc:8200"),
			Role:    envOrDefault("OPENBAO_ROLE", "vault-notify-proxy"),
			JWTPath: podid.DefaultTokenPath,
		}},
		Send: func(ctx context.Context, webhookURL string, n Notification) error {
			return SendDiscord(ctx, discordClient, webhookURL, n)
		},
		Logger: logger,
	})

	if !isStrictLoopbackAddr(proxy.cfg.AdminAddr) {
		fatal(logger, "ADMIN_ADDR must be strictly loopback (127.0.0.1)", "admin_addr", proxy.cfg.AdminAddr)
	}

	go func() {
		logger.Info("loopback admin listener starting", "addr", proxy.cfg.AdminAddr)
		srv := &http.Server{
			Addr:              proxy.cfg.AdminAddr,
			Handler:           proxy.AdminMux(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      45 * time.Second, // set-webhook does an OpenBao login + write
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(logger, "admin listener failed", "error", err.Error())
		}
	}()

	logger.Info("cluster data listener starting", "addr", proxy.cfg.DataAddr, "grants", fmt.Sprint(len(grants)))
	dataSrv := &http.Server{
		Addr:              proxy.cfg.DataAddr,
		Handler:           proxy.DataMux(),
		ReadHeaderTimeout: 10 * time.Second,
		// SendDiscord can block ~60 s on capped 429 waits.
		WriteTimeout: 120 * time.Second,
	}
	if err := dataSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, "data listener failed", "error", err.Error())
	}
}
