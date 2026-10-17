package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type workerStore struct {
	e                 ClaimedEvent
	published, failed bool
	dead              bool
	delay             time.Duration
}

func (r *workerStore) Claim(context.Context, time.Duration) (ClaimedEvent, bool, error) {
	return r.e, true, nil
}
func (r *workerStore) Published(context.Context, ClaimedEvent) error { r.published = true; return nil }
func (r *workerStore) Failed(_ context.Context, _ ClaimedEvent, d time.Duration, dead bool) error {
	r.failed = true
	r.dead = dead
	r.delay = d
	return nil
}
func (r *workerStore) Prune(context.Context, time.Duration) (int64, error) { return 0, nil }

type publisherFunc func(context.Context, ClaimedEvent) error

func (f publisherFunc) Publish(ctx context.Context, e ClaimedEvent) error { return f(ctx, e) }

func TestOutboxAcknowledgementAndRetry(t *testing.T) {
	for _, failure := range []bool{false, true} {
		r := &workerStore{e: ClaimedEvent{Attempts: 3}}
		p := publisherFunc(func(context.Context, ClaimedEvent) error {
			if failure {
				return errors.New("broker down")
			}
			return nil
		})
		cfg := OutboxConfig{DBTimeout: time.Second, PublishTimeout: time.Second, Lease: time.Minute, RetryBase: time.Second, RetryMax: time.Minute, MaxAttempts: 3}
		found, err := NewOutboxWorker(r, p, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Step(context.Background())
		if err != nil || !found || r.published == failure || r.failed != failure || failure && !r.dead {
			t.Fatalf("ack/retry: %+v %v", r, err)
		}
	}
}

func TestOutboxShutdownLeavesAmbiguousClaim(t *testing.T) {
	r := &workerStore{}
	ctx, cancel := context.WithCancel(context.Background())
	p := publisherFunc(func(context.Context, ClaimedEvent) error { cancel(); return nil })
	_, err := NewOutboxWorker(r, p, OutboxConfig{DBTimeout: time.Second, PublishTimeout: time.Second}, slog.New(slog.NewTextHandler(io.Discard, nil))).Step(ctx)
	if !errors.Is(err, context.Canceled) || r.published || r.failed {
		t.Fatalf("claim must expire: %+v %v", r, err)
	}
}

func TestBackoffBounds(t *testing.T) {
	for attempt := 1; attempt <= 1000; attempt++ {
		d := RetryDelay(attempt, time.Second, time.Minute)
		if d < time.Second/2 || d > time.Minute || attempt > 7 && d < 30*time.Second {
			t.Fatalf("attempt %d delay %v", attempt, d)
		}
	}
}
