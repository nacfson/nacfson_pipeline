package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/kafkaclient"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

const (
	DefaultGroup              = "error-detector"
	DefaultValidTopic         = "logs.proj.valid"
	DefaultQuarantineTopic    = "logs.proj.quarantine"
	defaultRetryMin           = time.Second
	defaultRetryMax           = 30 * time.Second
	defaultCommitPollInterval = 500 * time.Millisecond
)

type Config struct {
	Client             kafkaclient.Config
	Group              string
	ValidTopic         string
	QuarantineTopic    string
	RetryMin           time.Duration
	RetryMax           time.Duration
	CommitPollInterval time.Duration
	CanCommit          func() bool
}

type groupConsumer interface {
	PollFetches(ctx context.Context) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	Ping(ctx context.Context) error
	Close()
}

type Source struct {
	cfg            Config
	client         groupConsumer
	skipped        atomic.Int64
	sessionHealthy atomic.Bool

	mu        sync.Mutex
	canCommit func() bool
	pending   []*kgo.Record
}

func NewSource(cfg Config) (*Source, error) {
	cfg = withDefaults(cfg)
	cl, err := kafkaclient.NewGroupConsumer(cfg.Client, cfg.Group, cfg.ValidTopic, cfg.QuarantineTopic)
	if err != nil {
		return nil, err
	}
	return &Source{cfg: cfg, client: cl, canCommit: cfg.CanCommit}, nil
}

func newSourceWithConsumer(cfg Config, consumer groupConsumer) *Source {
	cfg = withDefaults(cfg)
	return &Source{cfg: cfg, client: consumer, canCommit: cfg.CanCommit}
}

func withDefaults(cfg Config) Config {
	if strings.TrimSpace(cfg.Group) == "" {
		cfg.Group = DefaultGroup
	}
	if strings.TrimSpace(cfg.ValidTopic) == "" {
		cfg.ValidTopic = DefaultValidTopic
	}
	if strings.TrimSpace(cfg.QuarantineTopic) == "" {
		cfg.QuarantineTopic = DefaultQuarantineTopic
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = defaultRetryMin
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = defaultRetryMax
	}
	if cfg.CommitPollInterval <= 0 {
		cfg.CommitPollInterval = defaultCommitPollInterval
	}
	return cfg
}

func (s *Source) SetCanCommit(fn func() bool) {
	s.mu.Lock()
	s.canCommit = fn
	s.mu.Unlock()
}

func (s *Source) allowCommit() bool {
	s.mu.Lock()
	fn := s.canCommit
	s.mu.Unlock()
	if fn == nil {
		return true
	}
	return fn()
}

func (s *Source) hasPending() bool {
	s.mu.Lock()
	n := len(s.pending)
	s.mu.Unlock()
	return n > 0
}

func (s *Source) appendPending(rec *kgo.Record) {
	s.mu.Lock()
	s.pending = append(s.pending, rec)
	s.mu.Unlock()
}

func (s *Source) CommitPending(ctx context.Context) error {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := append([]*kgo.Record(nil), s.pending...)
	s.mu.Unlock()

	if err := s.commitWithRetry(ctx, batch); err != nil {
		return err
	}
	s.mu.Lock()
	if len(s.pending) >= len(batch) {
		s.pending = s.pending[len(batch):]
	} else {
		s.pending = nil
	}
	s.mu.Unlock()
	return nil
}

func (s *Source) Skipped() int64 { return s.skipped.Load() }

func (s *Source) Close() {
	if s != nil && s.client != nil {
		s.client.Close()
	}
}

func (s *Source) Ready(ctx context.Context) error {
	if !s.sessionHealthy.Load() {
		return errors.New("kafka: consumer session not healthy")
	}
	return s.client.Ping(ctx)
}

func (s *Source) Run(ctx context.Context, onRecord func(classify.Record) error, onQuarantine func(vlogs.QuarantineRecord) error) error {
	if err := s.client.Ping(ctx); err != nil {
		s.sessionHealthy.Store(false)
		return err
	}
	s.sessionHealthy.Store(true)
	for ctx.Err() == nil {
		if s.hasPending() && s.allowCommit() {
			if err := s.CommitPending(ctx); err != nil {
				return err
			}
		}

		pollCtx := ctx
		var cancel context.CancelFunc
		if s.hasPending() {
			pollCtx, cancel = context.WithTimeout(ctx, s.cfg.CommitPollInterval)
		}
		fetches := s.client.PollFetches(pollCtx)
		if cancel != nil {
			cancel()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			s.sessionHealthy.Store(true)
			if err := s.handleRecord(rec, onRecord, onQuarantine); err != nil {
				return err
			}
			s.appendPending(rec)
			if s.allowCommit() {
				if err := s.CommitPending(ctx); err != nil {
					return err
				}
			}
		}

		if s.hasPending() && s.allowCommit() {
			if err := s.CommitPending(ctx); err != nil {
				return err
			}
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, fe := range errs {
				if errors.Is(fe.Err, context.DeadlineExceeded) && ctx.Err() == nil {
					continue
				}
				s.sessionHealthy.Store(false)
				return fe.Err
			}
		}
	}
	return ctx.Err()
}

func (s *Source) commitWithRetry(ctx context.Context, recs []*kgo.Record) error {
	backoff := s.cfg.RetryMin
	for {
		err := s.client.CommitRecords(ctx, recs...)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !sleepCtx(ctx, backoff) {
			return ctx.Err()
		}
		backoff *= 2
		if backoff > s.cfg.RetryMax {
			backoff = s.cfg.RetryMax
		}
	}
}

func (s *Source) handleRecord(rec *kgo.Record, onRecord func(classify.Record) error, onQuarantine func(vlogs.QuarantineRecord) error) error {
	switch rec.Topic {
	case s.cfg.ValidTopic:
		r, ok := DecodeValid(rec.Value, rec.Timestamp)
		if !ok {
			s.skipped.Add(1)
			return nil
		}
		return onRecord(r)
	case s.cfg.QuarantineTopic:
		q, ok := DecodeQuarantine(rec.Value, rec.Timestamp)
		if !ok {
			s.skipped.Add(1)
			return nil
		}
		return onQuarantine(q)
	default:
		return nil
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

var dynamicKeyRe = regexp.MustCompile(`\d{4,}|[0-9a-fA-F]{32}`)

func parseTimestamp(primary, secondary string, fallback time.Time) time.Time {
	for _, raw := range []string{primary, secondary} {
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t.UTC()
		}
	}
	if !fallback.IsZero() {
		return fallback.UTC()
	}
	return time.Now().UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func decodeObject(line []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return obj, true
}

func flattenObject(prefix string, in map[string]any, out map[string]any, dynamicKey *bool) {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if dynamicKey != nil && dynamicKeyRe.MatchString(k) {
			*dynamicKey = true
		}
		name := k
		if prefix != "" {
			name = prefix + "." + k
		}
		if sub, ok := in[k].(map[string]any); ok {
			flattenObject(name, sub, out, dynamicKey)
			continue
		}
		if _, dup := out[name]; !dup {
			out[name] = in[k]
		}
	}
}

func flatStr(flat map[string]any, key string) string {
	switch v := flat[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func DecodeValid(line []byte, recordTime time.Time) (classify.Record, bool) {
	obj, ok := decodeObject(line)
	if !ok {
		return classify.Record{}, false
	}
	flat := make(map[string]any, len(obj))
	var dynamicKey bool
	flattenObject("", obj, flat, &dynamicKey)

	level := flatStr(flat, "level")
	parsed := level != ""
	timeRejected := parsed && flat["time"] != nil && flatStr(flat, "time") != ""
	msg := flatStr(flat, "msg")
	message := firstNonEmpty(flatStr(flat, "message"), flatStr(flat, "_msg"), msg)

	return classify.Record{
		Time:             parseTimestamp(flatStr(flat, "_time"), flatStr(flat, "observed_time"), recordTime),
		StreamID:         flatStr(flat, "_stream_id"),
		Project:          firstNonEmpty(flatStr(flat, "k8s.namespace.name"), flatStr(flat, "project")),
		Pod:              firstNonEmpty(flatStr(flat, "k8s.pod.name"), flatStr(flat, "pod")),
		Container:        firstNonEmpty(flatStr(flat, "k8s.container.name"), flatStr(flat, "container")),
		Node:             firstNonEmpty(flatStr(flat, "k8s.node.name"), flatStr(flat, "node")),
		Message:          message,
		Parsed:           parsed,
		Level:            level,
		Msg:              msg,
		EventName:        firstNonEmpty(flatStr(flat, "event.name"), flatStr(flat, "kind")),
		ServiceVersion:   flatStr(flat, "service.version"),
		TraceID:          flatStr(flat, "trace_id"),
		ExceptionType:    flatStr(flat, "exception.type"),
		ExceptionMessage: flatStr(flat, "exception.message"),
		Stacktrace:       flatStr(flat, "exception.stacktrace"),
		Fingerprint:      flatStr(flat, "error.fingerprint"),
		TimeRejected:     timeRejected,
		DynamicKey:       dynamicKey,
	}, true
}

func DecodeQuarantine(line []byte, recordTime time.Time) (vlogs.QuarantineRecord, bool) {
	obj, ok := decodeObject(line)
	if !ok {
		return vlogs.QuarantineRecord{}, false
	}
	flat := make(map[string]any, len(obj))
	flattenObject("", obj, flat, nil)

	observed := flatStr(flat, "observed_time")
	return vlogs.QuarantineRecord{
		Time:         parseTimestamp(flatStr(flat, "_time"), observed, recordTime),
		StreamID:     flatStr(flat, "_stream_id"),
		Project:      firstNonEmpty(flatStr(flat, "k8s.namespace.name"), flatStr(flat, "project")),
		Pod:          firstNonEmpty(flatStr(flat, "k8s.pod.name"), flatStr(flat, "pod")),
		Container:    firstNonEmpty(flatStr(flat, "k8s.container.name"), flatStr(flat, "container")),
		Node:         firstNonEmpty(flatStr(flat, "k8s.node.name"), flatStr(flat, "node")),
		Message:      firstNonEmpty(flatStr(flat, "msg"), flatStr(flat, "_msg"), flatStr(flat, "message")),
		IOStream:     flatStr(flat, "log.iostream"),
		ObservedTime: observed,
	}, true
}
