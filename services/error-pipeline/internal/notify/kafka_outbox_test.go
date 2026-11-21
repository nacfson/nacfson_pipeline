package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

type fakeSyncProducer struct {
	records []*kgo.Record
	err     error
	closed  bool
}

func (f *fakeSyncProducer) ProduceSync(_ context.Context, rs ...*kgo.Record) kgo.ProduceResults {
	res := make(kgo.ProduceResults, 0, len(rs))
	for _, r := range rs {
		if f.err != nil {
			res = append(res, kgo.ProduceResult{Record: r, Err: f.err})
			continue
		}
		f.records = append(f.records, r)
		res = append(res, kgo.ProduceResult{Record: r})
	}
	return res
}

func (f *fakeSyncProducer) Close() { f.closed = true }

func TestKafkaOutboxSendSerializesRedactedNotification(t *testing.T) {
	fp := &fakeSyncProducer{}
	outbox, err := newKafkaOutboxWithProducer("alerts.errors", fp)
	if err != nil {
		t.Fatal(err)
	}
	n := Notification{
		Title:    "New error in proj-pn/pn-backend password=supersecret",
		Severity: SeverityError,
		Fields:   []Field{{Name: "Project token=fieldnamesecret", Value: "proj-pn"}},
		Body:     "crash with Bearer bodytoken123",
		Attachment: &Attachment{
			Name:    "body.md",
			Content: "raw attachment password=attachsecret",
		},
	}
	if err := outbox.Send(context.Background(), "errors", n); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fp.records) != 1 {
		t.Fatalf("records = %d, want 1", len(fp.records))
	}
	rec := fp.records[0]
	if rec.Topic != "alerts.errors" {
		t.Errorf("topic = %q, want alerts.errors", rec.Topic)
	}
	if string(rec.Key) != "errors" {
		t.Errorf("key = %q, want errors", string(rec.Key))
	}
	for _, secret := range []string{"supersecret", "fieldnamesecret", "bodytoken123", "attachsecret"} {
		if strings.Contains(string(rec.Value), secret) {
			t.Errorf("unredacted secret %q in kafka outbox payload: %s", secret, rec.Value)
		}
	}
	var got Notification
	if err := json.Unmarshal(rec.Value, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Severity != SeverityError || !strings.Contains(got.Body, "[REDACTED]") || !strings.Contains(got.Title, "[REDACTED]") {
		t.Errorf("decoded = %+v", got)
	}
	if got.Attachment == nil || !strings.Contains(got.Attachment.Content, "[REDACTED]") {
		t.Errorf("attachment = %+v", got.Attachment)
	}
	outbox.Close()
	if !fp.closed {
		t.Error("Close did not close underlying producer")
	}
}

func TestKafkaOutboxPropagatesProduceSyncError(t *testing.T) {
	wantErr := errors.New("broker unavailable")
	fp := &fakeSyncProducer{err: wantErr}
	outbox, err := newKafkaOutboxWithProducer("alerts.errors", fp)
	if err != nil {
		t.Fatal(err)
	}
	err = outbox.Send(context.Background(), "errors", Notification{Title: "t", Severity: SeverityError})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
