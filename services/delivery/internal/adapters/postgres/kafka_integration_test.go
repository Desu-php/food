package postgres

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	commands "example.com/food/contracts/delivery/commands/v1"
	events "example.com/food/contracts/orders/v1"
	"example.com/food/delivery/internal/adapters/kafka"
	"example.com/food/delivery/internal/application"
)

func TestKafkaFailureReplayAndDuplicateInbox(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("set TEST_KAFKA_BROKERS to a dedicated test broker")
	}
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	topic := "delivery-test-" + events.NewID()
	group := "delivery-test-" + events.NewID()
	producer, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	req := kmsg.NewPtrCreateTopicsRequest()
	req.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}}
	resp, err := req.RequestWith(ctx, producer)
	if err != nil || resp.Topics[0].ErrorCode != 0 {
		t.Fatal(resp, err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := kmsg.NewPtrDeleteTopicsRequest()
		req.Topics = []kmsg.DeleteTopicsRequestTopic{{Topic: &topic}}
		_, _ = req.RequestWith(ctx, producer)
	}()
	event := createCommand(42, 1, commands.CreateDelivery)
	body, _ := json.Marshal(event)
	if err = producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte("42"), Value: body}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	source, err := kafka.NewConsumer(strings.Split(brokers, ","), topic, group, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if source != nil {
			source.Close()
		}
	}()
	m, err := source.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	offset := m.Offset
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_events CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err = application.NewConsumer(source, r).WithTopics(events.Topic, topic).Process(ctx, m); err == nil {
		t.Fatal("failed transaction acknowledged")
	}
	source.Close()
	source = nil
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_events`); err != nil {
		t.Fatal(err)
	}
	source, err = kafka.NewConsumer(strings.Split(brokers, ","), topic, group, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m, err = source.Poll(ctx)
	if err != nil || m.Offset != offset {
		t.Fatal("lost uncommitted event", m, err)
	}
	// Simulate a crash after DB commit but before offset commit.
	if err = r.ProcessCommand(ctx, event); err != nil {
		t.Fatal(err)
	}
	source.Close()
	source = nil
	source, err = kafka.NewConsumer(strings.Split(brokers, ","), topic, group, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m, err = source.Poll(ctx)
	if err != nil || m.Offset != offset {
		t.Fatal(m, err)
	}
	if err = application.NewConsumer(source, r).WithTopics(events.Topic, topic).Process(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err = producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte("42"), Value: body}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	m, err = source.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = application.NewConsumer(source, r).WithTopics(events.Topic, topic).Process(ctx, m); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, table := range []string{"deliveries", "command_inbox", "outbox_events"} {
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 1 {
			t.Fatal(table, n, err)
		}
	}
}
