package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

func TestConsumerQuarantineRestartReplayAndConcurrentFence(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewConsumerInbox(pool, logger)
	var effects atomic.Int64
	handle := func(_ context.Context, m application.Message) error {
		if string(m.Body) == "poison" {
			return events.ErrInvalid
		}
		effects.Add(1)
		return nil
	}
	poison := application.Message{Topic: "test", Partition: 1, Offset: 10, Key: []byte("42"), Body: []byte("poison")}
	if err := store.Process(ctx, poison, handle); err != nil {
		t.Fatal(err)
	}
	// A second instance after restart observes the durable fence on every topic.
	restarted := NewConsumerInbox(pool, logger)
	dependent := application.Message{Topic: "commands", Partition: 2, Offset: 11, Key: []byte("42"), Body: []byte("dependent")}
	if err := restarted.Process(ctx, dependent, handle); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 0 {
		t.Fatal("dependent effects escaped quarantine")
	}
	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM consumer_quarantine WHERE topic='commands'`).Scan(&reason); err != nil || reason != "blocked_by_quarantine" {
		t.Fatal(reason, err)
	}
	// Duplicate after Kafka offset failure does not create a second parking record.
	if err := store.Process(ctx, poison, handle); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_quarantine`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	// Failed replay cannot resolve the fence; original bytes remain immutable.
	if err := store.Replay(ctx, 1, "operator", "investigating original event", nil, nil, handle); err == nil {
		t.Fatal("poison replay succeeded")
	}
	if err := store.Replay(ctx, 1, "", "missing audit", []byte("fixed"), nil, handle); err == nil {
		t.Fatal("unaudited replay succeeded")
	}
	if err := store.Replay(ctx, 1, "operator", "recovered original contract from upstream audit", []byte("fixed"), nil, handle); err != nil {
		t.Fatal(err)
	}
	if err := store.Replay(ctx, 2, "operator", "replay dependent after prerequisite", nil, nil, handle); err != nil {
		t.Fatal(err)
	}
	if err := store.Process(ctx, poison, handle); err != nil {
		t.Fatal(err)
	} // Resolved offset retry cannot resurrect original poison.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_quarantine WHERE resolved_at IS NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	var original []byte
	if err := pool.QueryRow(ctx, `SELECT body FROM consumer_quarantine WHERE id=1`).Scan(&original); err != nil || string(original) != "poison" {
		t.Fatal("original overwritten", err)
	}
	if effects.Load() != 2 {
		t.Fatal("wrong replay effects", effects.Load())
	}
	// Concurrent workers serialize decisions under the same persistent fence lock.
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := application.Message{Topic: "concurrent", Offset: int64(n), Body: []byte("ok")}
			if err := NewConsumerInbox(pool, logger).Process(ctx, m, handle); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if effects.Load() != 10 {
		t.Fatal("healthy processing did not resume")
	}
}

func TestConsumerQuarantineFailureNeverAcknowledges(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := pool.Exec(ctx, `ALTER TABLE consumer_quarantine ADD CONSTRAINT reject_parking CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	m := application.Message{Topic: "test", Body: []byte("bad")}
	err := NewConsumerInbox(pool, logger).Process(ctx, m, func(context.Context, application.Message) error { return events.ErrInvalid })
	if err == nil {
		t.Fatal("lost quarantine failure; caller would commit offset")
	}
}

func TestConsumerRetryRecoversTemporaryFailure(t *testing.T) {
	pool := integrationPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	err := NewConsumerInbox(pool, logger).Process(context.Background(), application.Message{Topic: "retry"}, func(context.Context, application.Message) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary unavailable")
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
}

func TestConsumerFenceLockHonorsCancellation(t *testing.T) {
	pool := integrationPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewConsumerInbox(pool, logger)
	_, release, err := store.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var calls int
	err = store.Process(ctx, application.Message{}, func(context.Context, application.Message) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("cancelled lock performed business operation", err, calls)
	}
}

func TestQuarantineDowngradePreservesAudit(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO consumer_quarantine(topic,partition,offset_value,message_key,body,reason,attempts) VALUES('test',0,1,'','', 'invalid_contract',1)`); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../../migrations/000004_consumer_quarantine.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("downgrade deleted quarantine/audit")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM consumer_quarantine`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
