package application

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
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
	ProcessOrder(context.Context, events.Event) error
	ProcessCommand(context.Context, commands.Command) error
}
type Consumer struct {
	processor                func(context.Context, Message) error
	source                   MessageSource
	repo                     InboxRepository
	logger                   *slog.Logger
	orderTopic, commandTopic string
}

func NewConsumer(source MessageSource, repo InboxRepository, logger ...*slog.Logger) *Consumer {
	c := &Consumer{source: source, repo: repo, orderTopic: events.Topic, commandTopic: commands.Topic}
	if len(logger) > 0 {
		c.logger = logger[0]
	}
	return c
}

// Handle applies one record without acknowledging Kafka. Production bootstrap
// adds bounded retries and durable parking; business effects share the Inbox transaction.
func (c *Consumer) Handle(ctx context.Context, m Message) (err error) {
	if m.Topic == c.commandTopic {
		command, decodeErr := commands.Decode(m.Body)
		defer func() {
			if c.logger != nil && err != nil {
				c.logger.Error("fulfillment command failed; offset retained", "topic", m.Topic, "partition", m.Partition, "offset", m.Offset)
			}
		}()
		if decodeErr != nil {
			return decodeErr
		}
		if string(m.Key) != strconv.FormatInt(command.AggregateID, 10) {
			return commands.ErrInvalid
		}
		ctx = correlation.WithID(ctx, command.CorrelationID)
		if err = c.repo.ProcessCommand(ctx, command); err != nil {
			return err
		}
		if c.logger != nil {
			c.logger.Info("fulfillment command processed", "message_id", command.MessageID, "command_type", command.CommandType, "order_id", command.AggregateID, "correlation_id", command.CorrelationID)
		}
		return nil
	}
	if m.Topic != c.orderTopic {
		return events.ErrInvalid
	}
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
	if err = c.repo.ProcessOrder(ctx, e); err != nil {
		return err
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
			return err
		}
		if c.processor != nil {
			err = c.processor(ctx, m)
		} else {
			op, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = c.Process(op, m)
			cancel()
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// WithTopics returns a configured copy; configuration remains immutable while Run executes.
func (c Consumer) WithTopics(orderTopic, commandTopic string) *Consumer {
	c.orderTopic = orderTopic
	c.commandTopic = commandTopic
	return &c
}

// Process acknowledges only after the application transaction completes.
func (c *Consumer) Process(ctx context.Context, m Message) error {
	if err := c.Handle(ctx, m); err != nil {
		return err
	}
	return c.source.Commit(ctx, m)
}
func (c Consumer) WithProcessor(processor func(context.Context, Message) error) *Consumer {
	c.processor = processor
	return &c
}
