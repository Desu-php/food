package kafka

import (
	"context"
	"fmt"
	"time"

	"example.com/food/notifications/internal/application"
	"github.com/twmb/franz-go/pkg/kgo"
)

type Consumer struct{ client *kgo.Client }

func NewConsumer(brokers []string, topic, group string, maxPoll time.Duration) (*Consumer, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("notifications"),
		kgo.ConsumeTopics(topic), kgo.ConsumerGroup(group), kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(), kgo.RebalanceTimeout(maxPoll), kgo.SessionTimeout(30*time.Second),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.DialTimeout(5*time.Second),
		kgo.FetchMaxBytes(1<<20), kgo.FetchMaxPartitionBytes(1<<20))
	if err != nil {
		return nil, fmt.Errorf("initialize kafka consumer: %w", err)
	}
	return &Consumer{client}, nil
}

func (c *Consumer) Poll(ctx context.Context) (application.Message, error) {
	// Release the previous single record only after application processing/commit.
	c.client.AllowRebalance()
	for {
		fetches := c.client.PollRecords(ctx, 1)
		if ctx.Err() != nil {
			return application.Message{}, ctx.Err()
		}
		if err := fetches.Err(); err != nil {
			return application.Message{}, err
		}
		for _, r := range fetches.Records() {
			return application.Message{Topic: r.Topic, Partition: r.Partition, Offset: r.Offset, LeaderEpoch: r.LeaderEpoch, Key: r.Key, Body: r.Value}, nil
		}
		c.client.AllowRebalance()
	}
}

func (c *Consumer) Commit(ctx context.Context, m application.Message) error {
	return c.client.CommitRecords(ctx, &kgo.Record{Topic: m.Topic, Partition: m.Partition, Offset: m.Offset, LeaderEpoch: m.LeaderEpoch})
}

func (c *Consumer) Ping(ctx context.Context) error { return c.client.Ping(ctx) }
func (c *Consumer) Close()                         { c.client.CloseAllowingRebalance() }
