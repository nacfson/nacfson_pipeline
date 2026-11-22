package opsrelay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/kafkaclient"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
)

const (
	DefaultErrorsGroup   = "ops-relay"
	DefaultErrorsChannel = "errors"
	defaultSendTimeout   = 15 * time.Second
	defaultCommitTimeout = 5 * time.Second
	defaultRetryStep     = 5 * time.Second
	defaultRetryCap      = time.Minute
)

type ErrorsSender interface {
	Send(ctx context.Context, channel string, n notify.Notification) error
}

type errorsConsumer interface {
	PollFetches(ctx context.Context) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	Close()
}

type ErrorsRelayConfig struct {
	Topic       string
	Group       string
	Channel     string
	SendTimeout time.Duration
	RetryStep   time.Duration
	RetryCap    time.Duration
}

type ErrorsRelay struct {
	cfg      ErrorsRelayConfig
	consumer errorsConsumer
	sender   ErrorsSender
}

func NewErrorsRelay(kc kafkaclient.Config, cfg ErrorsRelayConfig, sender ErrorsSender) (*ErrorsRelay, error) {
	cfg = errorsRelayDefaults(cfg)
	if cfg.Topic == "" {
		return nil, errors.New("opsrelay: alerts errors topic is required")
	}
	if sender == nil {
		return nil, errors.New("opsrelay: errors sender is required")
	}
	cl, err := kafkaclient.NewGroupConsumer(kc, cfg.Group, cfg.Topic)
	if err != nil {
		return nil, err
	}
	return &ErrorsRelay{cfg: cfg, consumer: cl, sender: sender}, nil
}

func newErrorsRelayWithConsumer(cfg ErrorsRelayConfig, consumer errorsConsumer, sender ErrorsSender) *ErrorsRelay {
	return &ErrorsRelay{cfg: errorsRelayDefaults(cfg), consumer: consumer, sender: sender}
}

func errorsRelayDefaults(cfg ErrorsRelayConfig) ErrorsRelayConfig {
	cfg.Topic = strings.TrimSpace(cfg.Topic)
	if strings.TrimSpace(cfg.Group) == "" {
		cfg.Group = DefaultErrorsGroup
	}
	if strings.TrimSpace(cfg.Channel) == "" {
		cfg.Channel = DefaultErrorsChannel
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = defaultSendTimeout
	}
	if cfg.RetryStep <= 0 {
		cfg.RetryStep = defaultRetryStep
	}
	if cfg.RetryCap <= 0 {
		cfg.RetryCap = defaultRetryCap
	}
	return cfg
}

func (r *ErrorsRelay) Close() {
	if r != nil && r.consumer != nil {
		r.consumer.Close()
	}
}

func (r *ErrorsRelay) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		fetches := r.consumer.PollFetches(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			if err := r.processRecord(ctx, rec); err != nil {
				return err
			}
			if err := r.commitDelivered(rec); err != nil {
				return err
			}
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			return errs[0].Err
		}
	}
	return ctx.Err()
}

func (r *ErrorsRelay) processRecord(ctx context.Context, rec *kgo.Record) error {
	var n notify.Notification
	if err := json.Unmarshal(rec.Value, &n); err != nil || strings.TrimSpace(n.Title) == "" {
		slog.Warn("dropping malformed alerts.errors record", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset)
		return nil
	}
	n = notify.Sanitize(n)
	for attempt := 1; ; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, r.cfg.SendTimeout)
		err := r.sender.Send(sctx, r.cfg.Channel, n)
		cancel()
		if err == nil {
			slog.Info("relayed error notification", "channel", r.cfg.Channel, "title", n.Title, "offset", rec.Offset)
			return nil
		}
		var status *notify.StatusError
		if errors.As(err, &status) && status.Permanent() {
			slog.Error("notify permanently rejected error notification, dropping", "channel", r.cfg.Channel, "err", err, "offset", rec.Offset)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("notify failed for alerts.errors, will retry", "channel", r.cfg.Channel, "err", err, "attempt", attempt, "offset", rec.Offset)
		wait := time.Duration(attempt) * r.cfg.RetryStep
		if wait > r.cfg.RetryCap {
			wait = r.cfg.RetryCap
		}
		if !sleepRelay(ctx, wait) {
			return ctx.Err()
		}
	}
}

func (r *ErrorsRelay) commitDelivered(rec *kgo.Record) error {
	cctx, cancel := context.WithTimeout(context.Background(), defaultCommitTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := r.consumer.CommitRecords(cctx, rec)
		if err == nil {
			return nil
		}
		if cctx.Err() != nil {
			return err
		}
		wait := time.Duration(attempt) * r.cfg.RetryStep
		if wait > r.cfg.RetryCap {
			wait = r.cfg.RetryCap
		}
		if !sleepRelay(cctx, wait) {
			return err
		}
	}
}

func RunErrorsRelayLoop(ctx context.Context, kc kafkaclient.Config, cfg ErrorsRelayConfig, sender ErrorsSender) {
	for ctx.Err() == nil {
		er, err := NewErrorsRelay(kc, cfg, sender)
		if err != nil {
			slog.Warn("alerts.errors consumer init failed, retrying", "err", err)
			sleepRelay(ctx, 10*time.Second)
			continue
		}
		err = er.Run(ctx)
		er.Close()
		if err != nil && ctx.Err() == nil {
			slog.Warn("alerts.errors consumer stopped, restarting", "err", err)
			sleepRelay(ctx, 10*time.Second)
		}
	}
}

func sleepRelay(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
