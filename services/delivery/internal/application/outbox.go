package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

var ErrLeaseLost = errors.New("outbox lease lost")

type ClaimedEvent struct {
	ID            string
	AggregateID   int64
	Version       int64
	CorrelationID string
	Body          []byte
	Token         string
	Attempts      int
	Recovered     bool
}

type OutboxRepository interface {
	Claim(context.Context, time.Duration) (ClaimedEvent, bool, error)
	Published(context.Context, ClaimedEvent) error
	Failed(context.Context, ClaimedEvent, time.Duration, bool) error
	Prune(context.Context, time.Duration) (int64, error)
}

type EventPublisher interface {
	Publish(context.Context, ClaimedEvent) error
}

type OutboxConfig struct {
	PollInterval    time.Duration
	Lease           time.Duration
	PublishTimeout  time.Duration
	DBTimeout       time.Duration
	RetryBase       time.Duration
	RetryMax        time.Duration
	MaxAttempts     int
	Retention       time.Duration
	CleanupInterval time.Duration
}

type OutboxWorker struct {
	repo      OutboxRepository
	publisher EventPublisher
	cfg       OutboxConfig
	logger    *slog.Logger
}

func errorType(err error) string { return fmt.Sprintf("%T", err) }

func NewOutboxWorker(repo OutboxRepository, publisher EventPublisher, cfg OutboxConfig, logger *slog.Logger) *OutboxWorker {
	return &OutboxWorker{repo: repo, publisher: publisher, cfg: cfg, logger: logger}
}

// RetryDelay uses capped exponential backoff with equal jitter, without overflow.
func RetryDelay(attempt int, base, maximum time.Duration) time.Duration {
	d := min(base, maximum)
	for i := 1; i < attempt && d < maximum; i++ {
		if d > maximum/2 {
			d = maximum
		} else {
			d *= 2
		}
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

// Step claims one record so its lease is not spent waiting in a local batch.
func (w *OutboxWorker) Step(ctx context.Context) (bool, error) {
	dbctx, cancel := context.WithTimeout(ctx, w.cfg.DBTimeout)
	e, found, err := w.repo.Claim(dbctx, w.cfg.Lease)
	cancel()
	if err != nil || !found {
		return false, err
	}
	logger := w.logger.With("event_id", e.ID, "order_id", e.AggregateID, "aggregate_version", e.Version, "correlation_id", e.CorrelationID, "attempt", e.Attempts)
	if e.Recovered {
		logger.Warn("outbox expired lease recovered")
	}
	pubctx, cancel := context.WithTimeout(ctx, w.cfg.PublishTimeout)
	err = w.publisher.Publish(pubctx, e)
	cancel()
	// On shutdown leave the claim for recovery; the publication outcome may be unknown.
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	dbctx, cancel = context.WithTimeout(ctx, w.cfg.DBTimeout)
	defer cancel()
	if err != nil {
		dead := e.Attempts >= w.cfg.MaxAttempts
		delay := RetryDelay(e.Attempts, w.cfg.RetryBase, w.cfg.RetryMax)
		logger.Warn("kafka publication failed", "error_type", errorType(err), "retry_in", delay, "dead", dead)
		return true, w.repo.Failed(dbctx, e, delay, dead)
	}
	if err := w.repo.Published(dbctx, e); err != nil {
		return true, err
	}
	logger.Info("outbox event published")
	return true, nil
}

func (w *OutboxWorker) Run(ctx context.Context) error {
	cleanup := time.NewTicker(w.cfg.CleanupInterval)
	defer cleanup.Stop()
	for ctx.Err() == nil {
		select {
		case <-cleanup.C:
			dbctx, cancel := context.WithTimeout(ctx, w.cfg.DBTimeout)
			count, err := w.repo.Prune(dbctx, w.cfg.Retention)
			cancel()
			if err != nil {
				w.logger.Error("outbox cleanup failed", "error_type", errorType(err))
			} else if count > 0 {
				w.logger.Info("outbox records pruned", "count", count)
			}
		default:
		}
		found, err := w.Step(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			w.logger.Error("outbox processing failed", "error_type", errorType(err))
		}
		if !found || err != nil {
			timer := time.NewTimer(w.cfg.PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return nil
}
