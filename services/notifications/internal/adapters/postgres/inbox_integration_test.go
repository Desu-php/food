package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/notifications/internal/adapters/kafka"
	"example.com/food/notifications/internal/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("notifications_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	body, err := os.ReadFile("../../../migrations/000001_inbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"000002_fulfillment", "000003_saga", "000004_pickup"} {
		body, err := os.ReadFile("../../../migrations/" + name + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func message(t *testing.T, kind string, id int64) application.Message {
	t.Helper()
	version := int64(1)
	if kind == events.OrderCancelled {
		version = 2
	}
	e := events.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: id, AggregateVersion: version, CorrelationID: "integration-request", OccurredAt: time.Now().UTC(), Payload: events.Payload{OrderID: id, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 25000, Currency: "TJS"}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return application.Message{Topic: events.Topic, Key: []byte(strconv.FormatInt(id, 10)), Body: body}
}

func TestInboxConcurrentDuplicatesAndReorderedVersions(t *testing.T) {
	pool := integrationPool(t)
	repo := NewInbox(pool)
	svc := application.NewNotifications(repo)
	ctx := context.Background()
	created := message(t, events.OrderCreated, 42)
	cancelled := message(t, events.OrderCancelled, 42)
	// Each event is a separate intent. Cancellation arriving first must not cause
	// the older, delayed creation intent to disappear or replace cancellation.
	if _, _, err := svc.Handle(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.Handle(ctx, created); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var total, pending int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='PENDING') FROM notifications`).Scan(&total, &pending); err != nil || total != 2 || pending != 2 {
		t.Fatalf("total=%d pending=%d err=%v", total, pending, err)
	}
	conflict := message(t, events.OrderCreated, 42)
	if _, _, err := svc.Handle(ctx, conflict); !errors.Is(err, application.ErrEventConflict) {
		t.Fatalf("version conflict accepted: %v", err)
	}
	var e events.Event
	if err := json.Unmarshal(created.Body, &e); err != nil {
		t.Fatal(err)
	}
	e.Payload.CustomerID++
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	created.Body = body
	if _, _, err := svc.Handle(ctx, created); !errors.Is(err, application.ErrEventConflict) {
		t.Fatalf("event identity conflict accepted: %v", err)
	}
}

func TestInboxRollbackAndReplay(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	repo := NewInbox(pool)
	svc := application.NewNotifications(repo)
	if _, err := pool.Exec(ctx, `ALTER TABLE notifications ADD CONSTRAINT reject_intents CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	m := message(t, events.OrderCreated, 42)
	if _, _, err := svc.Handle(ctx, m); err == nil {
		t.Fatal("accepted failed business write")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM processed_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("inbox survived rollback: %d %v", count, err)
	}
	if err := repo.Quarantine(ctx, m, "processing_retries_exhausted", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE notifications DROP CONSTRAINT reject_intents`); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.FailedMessage(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Handle(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := repo.Resolve(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := svc.Handle(ctx, failed); err != nil || !duplicate {
		t.Fatalf("replay idempotency: %v %v", duplicate, err)
	}
}

func TestInboxMigrationRoundTrip(t *testing.T) {
	pool := integrationPool(t)
	for _, suffix := range []string{"down", "up"} {
		body, err := os.ReadFile("../../../migrations/000001_inbox." + suffix + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000002_fulfillment", "000003_saga", "000004_pickup"} {
		body, err := os.ReadFile("../../../migrations/" + name + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := application.NewNotifications(NewInbox(pool)).Handle(context.Background(), message(t, events.OrderCreated, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestKafkaInboxCommitAfterPersistence(t *testing.T) {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		t.Skip("set TEST_KAFKA_BROKERS to a dedicated Redpanda broker")
	}
	pool := integrationPool(t)
	repo := NewInbox(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	topic := "notifications-test-" + events.NewID()
	group := "notifications-test-" + events.NewID()
	producer, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(brokers, ",")...))
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	request := kmsg.NewPtrCreateTopicsRequest()
	request.Topics = []kmsg.CreateTopicsRequestTopic{{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}}
	response, err := request.RequestWith(ctx, producer)
	if err != nil || response.Topics[0].ErrorCode != 0 {
		t.Fatalf("create topic: %v %v", response, err)
	}
	defer func() {
		req := kmsg.NewPtrDeleteTopicsRequest()
		req.Topics = []kmsg.DeleteTopicsRequestTopic{{Topic: &topic}}
		_, _ = req.RequestWith(context.Background(), producer)
	}()
	m := message(t, events.OrderCreated, 42)
	if err := producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: m.Key, Value: m.Body}).FirstErr(); err != nil {
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
	m, err = source.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstOffset := m.Offset
	// Both business persistence and quarantine fail: no offset can be confirmed.
	for _, table := range []string{"notifications", "failed_messages"} {
		if _, err := pool.Exec(ctx, "ALTER TABLE "+table+" ADD CONSTRAINT reject_write CHECK(false) NOT VALID"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := application.ConsumerConfig{MaxAttempts: 1, OperationTimeout: time.Second, RetryBase: time.Millisecond, RetryMax: time.Millisecond}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := application.NewConsumer(source, repo, cfg, logger).Process(ctx, m); err == nil {
		t.Fatal("failed persistence was acknowledged")
	}
	source.Close()
	source = nil
	for _, table := range []string{"notifications", "failed_messages"} {
		if _, err := pool.Exec(ctx, "ALTER TABLE "+table+" DROP CONSTRAINT reject_write"); err != nil {
			t.Fatal(err)
		}
	}
	source, err = kafka.NewConsumer(strings.Split(brokers, ","), topic, group, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m, err = source.Poll(ctx)
	if err != nil || m.Offset != firstOffset {
		t.Fatalf("uncommitted message not redelivered: %+v %v", m, err)
	}
	c := application.NewConsumer(source, repo, cfg, logger)
	if err := c.Process(ctx, m); err != nil {
		t.Fatal(err)
	}
	cancelled := message(t, events.OrderCancelled, 42)
	for _, body := range [][]byte{m.Body, cancelled.Body, []byte(`{"schema_version":99}`), []byte(`{`)} {
		if err := producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: m.Key, Value: body}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		m, err := source.Poll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Process(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("notifications=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM failed_messages`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("quarantines=%d err=%v", count, err)
	}
	// Restarting the same group must continue beyond the committed poison records.
	source.Close()
	source = nil
	probe := message(t, events.OrderCreated, 43)
	if err := producer.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: probe.Key, Value: probe.Body}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	source, err = kafka.NewConsumer(strings.Split(brokers, ","), topic, group, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.Poll(ctx)
	if err != nil || string(got.Key) != "43" {
		t.Fatalf("committed offsets not retained: %+v %v", got, err)
	}
}
