package kafka

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"example.com/food/delivery/internal/application"
)

type Producer struct {
	client *kgo.Client
	topic  string
}

func NewProducer(brokers []string, topic string, timeout time.Duration) (*Producer, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("delivery-outbox"),
		kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordDeliveryTimeout(timeout),
		kgo.RequestTimeoutOverhead(time.Second), kgo.DialTimeout(5*time.Second),
		kgo.MaxBufferedRecords(1), kgo.ProducerBatchMaxBytes(64<<10))
	if err != nil {
		return nil, fmt.Errorf("initialize kafka producer: %w", err)
	}
	return &Producer{client: client, topic: topic}, nil
}

func (p *Producer) Publish(ctx context.Context, e application.ClaimedEvent) error {
	// franz-go enables producer idempotence by default; Inbox is still necessary
	// across producer restarts and retries after an ambiguous acknowledgement.
	r := &kgo.Record{Topic: p.topic, Key: []byte(strconv.FormatInt(e.AggregateID, 10)), Value: e.Body,
		Headers: []kgo.RecordHeader{{Key: "correlation_id", Value: []byte(e.CorrelationID)}}}
	return p.client.ProduceSync(ctx, r).FirstErr()
}

func (p *Producer) Ping(ctx context.Context) error { return p.client.Ping(ctx) }

func (p *Producer) Close() { p.client.Close() }
