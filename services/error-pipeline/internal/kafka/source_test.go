package kafka

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
	"nacfson_pipeline/services/error-pipeline/internal/vlogs"
)

type fakeConsumer struct {
	mu        sync.Mutex
	batches   []kgo.Fetches
	committed []*kgo.Record
	pingErr   error
	closed    bool
}

func (f *fakeConsumer) PollFetches(ctx context.Context) kgo.Fetches {
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

func (f *fakeConsumer) CommitRecords(_ context.Context, rs ...*kgo.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, rs...)
	return nil
}

func (f *fakeConsumer) Ping(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeConsumer) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeConsumer) committedOffsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, 0, len(f.committed))
	for _, r := range f.committed {
		out = append(out, r.Offset)
	}
	return out
}

func makeFetch(recs ...*kgo.Record) kgo.Fetches {
	byTopic := map[string][]*kgo.Record{}
	for _, r := range recs {
		byTopic[r.Topic] = append(byTopic[r.Topic], r)
	}
	var topics []kgo.FetchTopic
	for topic, list := range byTopic {
		topics = append(topics, kgo.FetchTopic{
			Topic: topic,
			Partitions: []kgo.FetchPartition{{
				Partition: 0,
				Records:   list,
			}},
		})
	}
	return kgo.Fetches{{Topics: topics}}
}

func TestDecodeValidMapsMsgForRawCrashesNestedObjectsAndNonStringTypes(t *testing.T) {
	ts := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)

	crashLine := []byte(`{"msg":"panic: runtime error: index out of range","k8s.namespace.name":"proj-pn","k8s.pod.name":"pn-1","k8s.container.name":"backend","k8s.node.name":"node-real","node":"spoofed-node","observed_time":"2026-11-21T08:58:00Z"}`)
	rec, ok := DecodeValid(crashLine, ts)
	if !ok {
		t.Fatal("DecodeValid rejected crash line")
	}
	if rec.Parsed {
		t.Error("raw crash without level must have Parsed=false")
	}
	if rec.Message != "panic: runtime error: index out of range" {
		t.Errorf("Message = %q, want raw panic from msg field", rec.Message)
	}
	if rec.Project != "proj-pn" || rec.Container != "backend" || rec.Pod != "pn-1" || rec.Node != "node-real" {
		t.Errorf("metadata = %+v", rec)
	}
	if !rec.Time.Equal(time.Date(2026, 11, 21, 8, 58, 0, 0, time.UTC)) {
		t.Errorf("Time = %v, want observed_time", rec.Time)
	}
	ev, kind := classify.Line(rec.Message)
	if kind != classify.CrashStart || ev.Level != "fatal" {
		t.Fatalf("classify.Line = (%+v, %v), want CrashStart/fatal", ev, kind)
	}

	nginxLine := []byte(`{"msg":"2026/11/21 09:00:00 [error] 12#0: *3 upstream timed out","k8s.namespace.name":"proj-pn","k8s.container.name":"frontend","observed_time":"2026-11-21T09:00:00Z"}`)
	nrec, ok := DecodeValid(nginxLine, ts)
	if !ok {
		t.Fatal("DecodeValid rejected nginx line")
	}
	nev, nkind := classify.Line(nrec.Message)
	if nkind != classify.Error || nev.Source != classify.SourceNginx {
		t.Fatalf("nginx classify.Line = (%+v, %v), want Error/nginx", nev, nkind)
	}

	nestedAndNumeric := []byte(`{"level":"error","msg":503,"time":1763715600,"event":{"name":"http.failed"},"exception":{"type":"SQLException","message":"deadlock","stacktrace":"at db.go:10"},"error":{"fingerprint":"fp-nested"},"meta":{"req_12345":"v"},"k8s.namespace.name":"proj-pn","k8s.container.name":"backend","observed_time":"2026-11-21T08:59:00Z"}`)
	jrec, ok := DecodeValid(nestedAndNumeric, ts)
	if !ok {
		t.Fatal("DecodeValid rejected nested/numeric JSON record")
	}
	if !jrec.Parsed || jrec.Level != "error" || jrec.Msg != "503" || !jrec.TimeRejected || !jrec.DynamicKey {
		t.Fatalf("nested/numeric fields = %+v", jrec)
	}
	if jrec.EventName != "http.failed" || jrec.ExceptionType != "SQLException" || jrec.ExceptionMessage != "deadlock" || jrec.Stacktrace != "at db.go:10" || jrec.Fingerprint != "fp-nested" {
		t.Fatalf("flattened nested fields = %+v", jrec)
	}
}

func TestDecodeQuarantineUsesObservedTimeAndPrefersK8sMetadata(t *testing.T) {
	recTime := time.Date(2026, 11, 21, 9, 5, 0, 0, time.UTC)
	line := []byte(`{"msg":"non-json junk","k8s.namespace.name":"proj-health","project":"spoof","k8s.container.name":"api","observed_time":"2026-11-21T09:01:00Z"}`)
	q, ok := DecodeQuarantine(line, recTime)
	if !ok {
		t.Fatal("DecodeQuarantine failed")
	}
	if q.Project != "proj-health" || q.Container != "api" || q.Message != "non-json junk" {
		t.Fatalf("quarantine = %+v", q)
	}
	if !q.Time.Equal(time.Date(2026, 11, 21, 9, 1, 0, 0, time.UTC)) {
		t.Errorf("Time = %v", q.Time)
	}
}

func TestSourceDrainsHealthyRecordsBeforeReturningPartialPartitionError(t *testing.T) {
	ts := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	partialErr := errors.New("partition 1 leader moving")
	mixed := kgo.Fetches{{
		Topics: []kgo.FetchTopic{{
			Topic: DefaultValidTopic,
			Partitions: []kgo.FetchPartition{
				{
					Partition: 0,
					Records: []*kgo.Record{
						{Topic: DefaultValidTopic, Partition: 0, Offset: 10, Timestamp: ts, Value: []byte(`{"level":"error","msg":"healthy-part","k8s.namespace.name":"proj-pn","k8s.container.name":"backend"}`)},
					},
				},
				{
					Partition: 1,
					Err:       partialErr,
				},
			},
		}},
	}}
	fc := &fakeConsumer{batches: []kgo.Fetches{mixed}}
	src := newSourceWithConsumer(Config{RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond}, fc)

	var seen []string
	err := src.Run(context.Background(), func(r classify.Record) error {
		seen = append(seen, r.Msg)
		return nil
	}, func(vlogs.QuarantineRecord) error { return nil })
	if !errors.Is(err, partialErr) {
		t.Fatalf("Run err = %v, want %v", err, partialErr)
	}
	if len(seen) != 1 || seen[0] != "healthy-part" {
		t.Fatalf("healthy partition records lost on partial error: seen=%v", seen)
	}
	if offs := fc.committedOffsets(); len(offs) != 1 || offs[0] != 10 {
		t.Fatalf("committed = %v, want [10]", offs)
	}
}

func TestSourceDefersCommitWhileCrashIsOpenUntilCanCommitTurnsTrue(t *testing.T) {
	ts := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	fc := &fakeConsumer{
		batches: []kgo.Fetches{
			makeFetch(
				&kgo.Record{Topic: DefaultValidTopic, Offset: 50, Timestamp: ts, Value: []byte(`{"msg":"panic: boom","k8s.namespace.name":"proj-pn","k8s.container.name":"worker"}`)},
			),
		},
	}
	var open atomic.Bool
	open.Store(true)
	src := newSourceWithConsumer(Config{
		RetryMin:           time.Millisecond,
		RetryMax:           2 * time.Millisecond,
		CommitPollInterval: 10 * time.Millisecond,
		CanCommit:          func() bool { return !open.Load() },
	}, fc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	handled := make(chan struct{})
	go func() {
		done <- src.Run(ctx, func(classify.Record) error {
			close(handled)
			return nil
		}, func(vlogs.QuarantineRecord) error { return nil })
	}()

	<-handled
	time.Sleep(30 * time.Millisecond)
	if offs := fc.committedOffsets(); len(offs) != 0 {
		t.Fatalf("committed while crash still open: %v", offs)
	}

	open.Store(false)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(fc.committedOffsets()) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if offs := fc.committedOffsets(); len(offs) != 1 || offs[0] != 50 {
		t.Fatalf("committed after flush = %v, want [50]", offs)
	}
}

func TestSourceDoesNotCommitOnCallbackError(t *testing.T) {
	ts := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	fc := &fakeConsumer{
		batches: []kgo.Fetches{
			makeFetch(
				&kgo.Record{Topic: DefaultValidTopic, Offset: 42, Timestamp: ts, Value: []byte(`{"level":"error","msg":"fail","k8s.namespace.name":"proj-pn","k8s.container.name":"backend"}`)},
			),
		},
	}
	src := newSourceWithConsumer(Config{RetryMin: 5 * time.Millisecond, RetryMax: 10 * time.Millisecond}, fc)

	err := src.Run(context.Background(), func(classify.Record) error {
		return context.Canceled
	}, func(vlogs.QuarantineRecord) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if offsets := fc.committedOffsets(); len(offsets) != 0 {
		t.Fatalf("committed offsets = %v, want empty on failed delivery", offsets)
	}
}

func TestSourceReadyReflectsSessionAndPing(t *testing.T) {
	fc := &fakeConsumer{}
	src := newSourceWithConsumer(Config{}, fc)
	if err := src.Ready(context.Background()); err == nil {
		t.Fatal("Ready succeeded before session started")
	}
	src.sessionHealthy.Store(true)
	if err := src.Ready(context.Background()); err != nil {
		t.Fatalf("Ready failed with healthy session: %v", err)
	}
	fc.pingErr = errors.New("broker down")
	if err := src.Ready(context.Background()); err == nil {
		t.Fatal("Ready succeeded when ping failed")
	}
}
