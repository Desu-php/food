package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
)

type MessageSource interface {
	Poll(context.Context) (Message, error)
	Commit(context.Context, Message) error
}

type ConsumerConfig struct {
	MaxAttempts      int
	OperationTimeout time.Duration
	RetryBase        time.Duration
	RetryMax         time.Duration
}

type Consumer struct {
	processor func(context.Context, Message) error
	source    MessageSource
	repo      InboxRepository
	service   *Notifications
	cfg       ConsumerConfig
	logger    *slog.Logger
}

func NewConsumer(source MessageSource, repo InboxRepository, cfg ConsumerConfig, logger *slog.Logger) *Consumer {
	return &Consumer{source: source, repo: repo, service: NewNotifications(repo), cfg: cfg, logger: logger}
}

func retryDelay(attempt int, base, maximum time.Duration) time.Duration {
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

// Process commits exactly this record after either the Inbox transaction or a
// durable quarantine transaction. It must return on offset commit errors: polling
// a later record could otherwise commit past an unprocessed record.
func (c *Consumer) Process(ctx context.Context, m Message) error {
	logger := c.logger.With("topic", m.Topic, "partition", m.Partition, "offset", m.Offset)
	var e events.Event
	var duplicate bool
	var err error
	attempt := 0
	for attempt < c.cfg.MaxAttempts {
		attempt++
		opctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
		e, duplicate, err = c.service.Handle(opctx, m)
		cancel()
		if err == nil || errors.Is(err, events.ErrInvalid) || errors.Is(err, events.ErrUnsupported) || errors.Is(err, ErrEventConflict) {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("notification processing retry", "attempt", attempt, "event_id", e.EventID, "correlation_id", e.CorrelationID, "error_type", fmt.Sprintf("%T", err))
		if attempt < c.cfg.MaxAttempts {
			timer := time.NewTimer(retryDelay(attempt, c.cfg.RetryBase, c.cfg.RetryMax))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		reason := "processing_retries_exhausted"
		switch {
		case errors.Is(err, events.ErrInvalid):
			reason = "invalid_event"
		case errors.Is(err, events.ErrUnsupported):
			reason = "unsupported_contract"
		case errors.Is(err, ErrEventConflict):
			reason = "event_conflict"
		}
		opctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
		err = c.repo.Quarantine(opctx, m, reason, attempt)
		cancel()
		if err != nil {
			return fmt.Errorf("persist quarantine: %w", err)
		}
		logger.Error("notification message quarantined", "reason", reason, "attempts", attempt)
	} else {
		message := "notification intent created"
		if duplicate {
			message = "duplicate event skipped"
		}
		logger.Info(message, "event_id", e.EventID, "order_id", e.AggregateID, "aggregate_version", e.AggregateVersion, "correlation_id", e.CorrelationID)
	}
	opctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()
	if err := c.source.Commit(opctx, m); err != nil {
		logger.Error("kafka offset commit failed", "error_type", fmt.Sprintf("%T", err))
		return fmt.Errorf("commit kafka offset: %w", err)
	}
	return nil
}

func (c *Consumer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		m, err := c.source.Poll(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.logger.Error("kafka poll failed", "error_type", fmt.Sprintf("%T", err))
			return fmt.Errorf("poll kafka: %w", err)
		}
		process := c.Process
		if c.processor != nil {
			process = c.processor
		}
		if err := process(ctx, m); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	return nil
}

func (c Consumer) WithProcessor(processor func(context.Context, Message) error) *Consumer {
	c.processor = processor
	return &c
}
