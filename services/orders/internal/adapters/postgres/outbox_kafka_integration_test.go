package postgres

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/orders/internal/adapters/kafka"
	"example.com/food/orders/internal/application"
)

func TestOutboxKafkaAcknowledgementCrashAndRecovery(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("set TEST_KAFKA_BROKERS to a dedicated Redpanda broker")
	}
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	topic := "outbox-test-" + events.NewID()
	client, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := kmsg.NewPtrCreateTopicsRequest()
	request.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}}
	response, err := request.RequestWith(ctx, client)
	if err != nil || response.Topics[0].ErrorCode != 0 {
		t.Fatalf("create topic %v %v", response, err)
	}
	defer func() {
		req := kmsg.NewPtrDeleteTopicsRequest()
		req.Topics = []kmsg.DeleteTopicsRequestTopic{{Topic: &topic}}
		_, _ = req.RequestWith(context.Background(), client)
	}()
	producer, err := kafka.NewProducer(strings.Split(brokers, ","), topic, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if _, err := NewOrders(pool).Create(ctx, "crash-test", strings.Repeat("a", 64), newOrder(t)); err != nil {
		t.Fatal(err)
	}
	repo := NewOutbox(pool)
	first, found, err := repo.Claim(ctx, time.Minute)
	if err != nil || !found {
		t.Fatal("claim", err)
	}
	if err := producer.Publish(ctx, first); err != nil {
		t.Fatal(err)
	}
	// The broker has acknowledged, but the process dies before Published.
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET lease_until=now()-interval '1 second' WHERE event_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	cfg := application.OutboxConfig{Lease: time.Minute, DBTimeout: time.Second, PublishTimeout: 5 * time.Second, RetryBase: time.Millisecond, RetryMax: time.Second, MaxAttempts: 3}
	if found, err := application.NewOutboxWorker(repo, producer, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Step(ctx); err != nil || !found {
		t.Fatalf("recover worker: %v %v", found, err)
	}
	reader, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...), kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {0: kgo.NewOffset().AtStart()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	count := 0
	for count < 2 {
		fetches := reader.PollRecords(ctx, 2-count)
		if err := fetches.Err(); err != nil {
			t.Fatal(err)
		}
		for _, record := range fetches.Records() {
			e, err := events.Decode(record.Value)
			if err != nil || e.EventID != first.ID || string(record.Key) == "" {
				t.Fatalf("duplicate identity %+v %v", e, err)
			}
			count++
		}
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM outbox_events WHERE event_id=$1`, first.ID).Scan(&status); err != nil || status != "PUBLISHED" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
