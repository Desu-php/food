package kafka

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Desu-php/food/services/orders/internal/application"
)

type Producer struct {
	client       *kgo.Client
	topic        string
	commandTopic string
}

func NewProducer(brokers []string, topic string, timeout time.Duration, commandTopic ...string) (*Producer, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("orders-outbox"),
		kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordDeliveryTimeout(timeout),
		kgo.RequestTimeoutOverhead(time.Second), kgo.DialTimeout(5*time.Second),
		kgo.MaxBufferedRecords(1), kgo.ProducerBatchMaxBytes(64<<10))
	if err != nil {
		return nil, fmt.Errorf("initialize kafka producer: %w", err)
	}
	ct := "delivery.commands.v1"
	if len(commandTopic) > 0 {
		ct = commandTopic[0]
	}
	return &Producer{client: client, topic: topic, commandTopic: ct}, nil
}

func (p *Producer) Publish(ctx context.Context, e application.ClaimedEvent) error {
	// franz-go enables producer idempotence by default; Inbox is still necessary
	// across producer restarts and retries after an ambiguous acknowledgement.
	topic := p.topic
	if e.Stream == "delivery_commands" {
		topic = p.commandTopic
	}
	r := &kgo.Record{Topic: topic, Key: []byte(strconv.FormatInt(e.AggregateID, 10)), Value: e.Body,
		Headers: []kgo.RecordHeader{{Key: "correlation_id", Value: []byte(e.CorrelationID)}}}
	return p.client.ProduceSync(ctx, r).FirstErr()
}

func (p *Producer) Ping(ctx context.Context) error { return p.client.Ping(ctx) }

func (p *Producer) Close() { p.client.Close() }
