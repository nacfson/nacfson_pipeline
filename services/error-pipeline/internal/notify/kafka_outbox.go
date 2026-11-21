package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/kafkaclient"
)

type syncProducer interface {
	ProduceSync(ctx context.Context, rs ...*kgo.Record) kgo.ProduceResults
	Close()
}

type KafkaOutbox struct {
	topic    string
	producer syncProducer
}

func NewKafkaOutbox(kc kafkaclient.Config, topic string) (*KafkaOutbox, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, errors.New("notify: kafka outbox topic is required")
	}
	cl, err := kafkaclient.NewProducer(kc)
	if err != nil {
		return nil, err
	}
	return &KafkaOutbox{topic: topic, producer: cl}, nil
}

func newKafkaOutboxWithProducer(topic string, producer syncProducer) (*KafkaOutbox, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, errors.New("notify: kafka outbox topic is required")
	}
	if producer == nil {
		return nil, errors.New("notify: kafka outbox producer is required")
	}
	return &KafkaOutbox{topic: topic, producer: producer}, nil
}

func (k *KafkaOutbox) Send(ctx context.Context, channel string, n Notification) error {
	payload, err := json.Marshal(finalize(n))
	if err != nil {
		return fmt.Errorf("notify: encode kafka outbox %s: %w", channel, err)
	}
	rec := &kgo.Record{
		Topic: k.topic,
		Key:   []byte(channel),
		Value: payload,
	}
	if err := k.producer.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("notify: produce %s: %w", k.topic, err)
	}
	return nil
}

func (k *KafkaOutbox) Close() {
	if k != nil && k.producer != nil {
		k.producer.Close()
	}
}
