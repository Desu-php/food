package application

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"example.com/food/contracts/correlation"
	events "example.com/food/contracts/delivery/v1"
)

var ErrEventConflict = errors.New("event identity or aggregate version conflict")
var ErrEventGap = errors.New("aggregate version gap or invalid event ordering")

type Message struct {
	Topic       string
	Partition   int32
	Offset      int64
	LeaderEpoch int32
	Key, Body   []byte
}
type MessageSource interface {
	Poll(context.Context) (Message, error)
	Commit(context.Context, Message) error
}
type InboxRepository interface {
	ProcessDelivery(context.Context, events.Event) error
}
type Consumer struct {
	source MessageSource
	repo   InboxRepository
	logger *slog.Logger
}

func NewConsumer(source MessageSource, repo InboxRepository, logger ...*slog.Logger) *Consumer {
	c := &Consumer{source: source, repo: repo}
	if len(logger) > 0 {
		c.logger = logger[0]
	}
	return c
}

// A failure stops consumption without acknowledging this record or any later offset.
// Restart/retry replays the same record; Inbox and business effects share one transaction.
func (c *Consumer) Process(ctx context.Context, m Message) (err error) {
	e, err := events.Decode(m.Body)
	defer func() {
		if c.logger == nil {
			return
		}
		logger := c.logger.With("topic", m.Topic, "partition", m.Partition, "offset", m.Offset)
		if e.Validate() == nil {
			logger = logger.With("event_id", e.EventID, "aggregate_id", e.AggregateID, "aggregate_version", e.AggregateVersion, "correlation_id", e.CorrelationID)
		}
		if err == nil {
			logger.Info("fulfillment event processed")
			return
		}
		reason := "storage_or_commit_failure"
		switch {
		case errors.Is(err, events.ErrInvalid):
			reason = "invalid_contract"
		case errors.Is(err, ErrEventGap):
			reason = "version_gap"
		case errors.Is(err, ErrEventConflict):
			reason = "event_conflict"
		}
		logger.Error("fulfillment event failed; offset retained", "reason", reason)
	}()
	if err != nil {
		return err
	}
	if string(m.Key) != strconv.FormatInt(e.AggregateID, 10) {
		return events.ErrInvalid
	}
	ctx = correlation.WithID(ctx, e.CorrelationID)
	if err = c.repo.ProcessDelivery(ctx, e); err != nil {
		return err
	}
	return c.source.Commit(ctx, m)
}
func (c *Consumer) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		m, err := c.source.Poll(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = c.Process(op, m)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}
