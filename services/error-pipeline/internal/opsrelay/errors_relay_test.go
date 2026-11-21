package opsrelay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/notify"
)

type fakeErrorsConsumer struct {
	mu        sync.Mutex
	batches   []kgo.Fetches
	committed []*kgo.Record
	closed    bool
}

func (f *fakeErrorsConsumer) PollFetches(ctx context.Context) kgo.Fetches {
	f.mu.Lock()
	if len(f.batches) > 0 {
		b := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		return b
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (f *fakeErrorsConsumer) CommitRecords(_ context.Context, rs ...*kgo.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, rs...)
	return nil
}

func (f *fakeErrorsConsumer) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeErrorsConsumer) committedOffsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, 0, len(f.committed))
	for _, r := range f.committed {
		out = append(out, r.Offset)
	}
	return out
}

func makeAlertFetch(recs ...*kgo.Record) kgo.Fetches {
	return kgo.Fetches{{
		Topics: []kgo.FetchTopic{{
			Topic: "alerts.errors",
			Partitions: []kgo.FetchPartition{{
				Partition: 0,
				Records:   recs,
			}},
		}},
	}}
}

func TestErrorsRelayPostsToErrorsChannelAndCommitsOnlyAfter2xx(t *testing.T) {
	var attempts atomic.Int32
	var gotPath string
	var gotNotif notify.Notification
	var mu sync.Mutex

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			http.Error(w, "upstream rate limited", http.StatusTooManyRequests)
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotNotif)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(notify.Notification{
		Title:    "New error in proj-pn/pn-backend",
		Severity: notify.SeverityError,
		Body:     "```\npanic: nil pointer\n```",
	})
	fc := &fakeErrorsConsumer{
		batches: []kgo.Fetches{
			makeAlertFetch(&kgo.Record{Topic: "alerts.errors", Partition: 0, Offset: 7, Value: payload}),
		},
	}

	er := newErrorsRelayWithConsumer(ErrorsRelayConfig{
		Topic:     "alerts.errors",
		Group:     "ops-relay",
		Channel:   "errors",
		RetryStep: time.Millisecond,
		RetryCap:  2 * time.Millisecond,
	}, fc, &notify.Client{BaseURL: srv.URL})

	done := make(chan error, 1)
	go func() { done <- er.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fc.committedOffsets()) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v", err)
	}

	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3 (2 transient 429 + 1 success)", attempts.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/notify/errors" {
		t.Errorf("path = %q, want /v1/notify/errors", gotPath)
	}
	if gotNotif.Title != "New error in proj-pn/pn-backend" {
		t.Errorf("notification = %+v", gotNotif)
	}
	if offs := fc.committedOffsets(); len(offs) != 1 || offs[0] != 7 {
		t.Fatalf("committed = %v, want [7]", offs)
	}
}

func TestErrorsRelayDropsPermanent4xxAndCommitsOffset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid payload", http.StatusBadRequest)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(notify.Notification{Title: "bad", Severity: notify.SeverityError})
	fc := &fakeErrorsConsumer{
		batches: []kgo.Fetches{
			makeAlertFetch(
				&kgo.Record{Topic: "alerts.errors", Offset: 1, Value: payload},
				&kgo.Record{Topic: "alerts.errors", Offset: 2, Value: []byte("{not-json")},
			),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	er := newErrorsRelayWithConsumer(ErrorsRelayConfig{
		Topic:     "alerts.errors",
		RetryStep: time.Millisecond,
		RetryCap:  2 * time.Millisecond,
	}, fc, &notify.Client{BaseURL: srv.URL})

	done := make(chan error, 1)
	go func() { done <- er.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(fc.committedOffsets()) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if offs := fc.committedOffsets(); len(offs) != 2 || offs[0] != 1 || offs[1] != 2 {
		t.Fatalf("committed = %v, want [1 2]", offs)
	}
}

func TestErrorsRelayDoesNotCommitWhenCanceledDuring5xxRetry(t *testing.T) {
	var hits atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) >= 2 {
			cancel()
		}
		http.Error(w, "downstream outage", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(notify.Notification{Title: "transient", Severity: notify.SeverityError})
	fc := &fakeErrorsConsumer{
		batches: []kgo.Fetches{
			makeAlertFetch(&kgo.Record{Topic: "alerts.errors", Offset: 99, Value: payload}),
		},
	}

	er := newErrorsRelayWithConsumer(ErrorsRelayConfig{
		Topic:     "alerts.errors",
		RetryStep: 5 * time.Millisecond,
		RetryCap:  10 * time.Millisecond,
	}, fc, &notify.Client{BaseURL: srv.URL})

	err := er.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if offs := fc.committedOffsets(); len(offs) != 0 {
		t.Fatalf("committed = %v, want empty when delivery did not succeed", offs)
	}
}

type afterSendFunc struct {
	inner ErrorsSender
	after func()
}

func (a afterSendFunc) Send(ctx context.Context, channel string, n notify.Notification) error {
	err := a.inner.Send(ctx, channel, n)
	if err == nil && a.after != nil {
		a.after()
	}
	return err
}

func TestErrorsRelayRedactsSecretsAndCommitsDeliveredRecordAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var gotNotif notify.Notification
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&gotNotif)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	payload, _ := json.Marshal(notify.Notification{
		Title:    "Error password=leakedtitle",
		Severity: notify.SeverityError,
		Fields:   []notify.Field{{Name: "Meta token=leakedname", Value: "Bearer leakedval"}},
		Body:     "raw body password=leakedbody",
		Attachment: &notify.Attachment{
			Name:    "body.md",
			Content: "attachment Bearer leakedatt",
		},
	})
	fc := &fakeErrorsConsumer{
		batches: []kgo.Fetches{
			makeAlertFetch(&kgo.Record{Topic: "alerts.errors", Offset: 77, Value: payload}),
		},
	}

	er := newErrorsRelayWithConsumer(ErrorsRelayConfig{
		Topic:     "alerts.errors",
		RetryStep: time.Millisecond,
		RetryCap:  2 * time.Millisecond,
	}, fc, afterSendFunc{inner: &notify.Client{BaseURL: srv.URL}, after: cancel})

	if err := er.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if offs := fc.committedOffsets(); len(offs) != 1 || offs[0] != 77 {
		t.Fatalf("delivered record was not committed after cancel: %v", offs)
	}
	mu.Lock()
	defer mu.Unlock()
	raw, _ := json.Marshal(gotNotif)
	for _, secret := range []string{"leakedtitle", "leakedname", "leakedval", "leakedbody", "leakedatt"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("GKE ops-relay forwarded unredacted secret %q: %s", secret, raw)
		}
	}
}
