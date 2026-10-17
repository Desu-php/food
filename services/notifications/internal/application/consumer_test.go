package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/notifications/internal/domain"
)

type sourceStub struct {
	commits     int
	commitError error
}

func (*sourceStub) Poll(context.Context) (Message, error)   { return Message{}, errors.New("unused") }
func (s *sourceStub) Commit(context.Context, Message) error { s.commits++; return s.commitError }

type inboxStub struct {
	fail           bool
	quarantineFail bool
	seen           map[string]bool
	effects        int
	quarantines    int
	reason         string
}

func (r *inboxStub) Process(_ context.Context, e events.Event, n domain.Notification) (bool, error) {
	if r.fail {
		return false, errors.New("database unavailable")
	}
	if r.seen[e.EventID] {
		return true, nil
	}
	r.seen[e.EventID] = true
	r.effects++
	return false, nil
}
func (r *inboxStub) Quarantine(_ context.Context, _ Message, reason string, _ int) error {
	if r.quarantineFail {
		return errors.New("database unavailable")
	}
	r.quarantines++
	r.reason = reason
	return nil
}

func testMessage(t *testing.T, kind string) Message {
	t.Helper()
	version := int64(1)
	if kind == events.OrderCancelled {
		version = 2
	}
	e := events.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: 42, AggregateVersion: version, CorrelationID: "test-request", OccurredAt: time.Now().UTC(), Payload: events.Payload{OrderID: 42, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 100, Currency: "TJS"}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return Message{Topic: events.Topic, Key: []byte("42"), Body: body}
}

func newTestConsumer(r *inboxStub, s *sourceStub) *Consumer {
	return NewConsumer(s, r, ConsumerConfig{MaxAttempts: 2, OperationTimeout: time.Second, RetryBase: time.Millisecond, RetryMax: time.Millisecond}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func TestConsumerCreatedDuplicateAndCancelled(t *testing.T) {
	r := &inboxStub{seen: map[string]bool{}}
	s := &sourceStub{}
	c := newTestConsumer(r, s)
	created := testMessage(t, events.OrderCreated)
	for _, m := range []Message{created, created, testMessage(t, events.OrderCancelled)} {
		if err := c.Process(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if r.effects != 2 || s.commits != 3 || r.quarantines != 0 {
		t.Fatalf("effects=%d commits=%d", r.effects, s.commits)
	}
}

func TestConsumerDatabaseFailureNeverCommits(t *testing.T) {
	r := &inboxStub{fail: true, quarantineFail: true}
	s := &sourceStub{}
	if err := newTestConsumer(r, s).Process(context.Background(), testMessage(t, events.OrderCreated)); err == nil || s.commits != 0 {
		t.Fatalf("error=%v commits=%d", err, s.commits)
	}
}

func TestConsumerQuarantinesPoisonMessages(t *testing.T) {
	for _, body := range []string{`{`, `{"schema_version":99}`} {
		r := &inboxStub{}
		s := &sourceStub{}
		if err := newTestConsumer(r, s).Process(context.Background(), Message{Body: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		if r.quarantines != 1 || s.commits != 1 || r.effects != 0 {
			t.Fatalf("quarantine=%d commits=%d", r.quarantines, s.commits)
		}
	}
}

func TestConsumerRetriesThenDurablyQuarantines(t *testing.T) {
	r := &inboxStub{fail: true}
	s := &sourceStub{}
	if err := newTestConsumer(r, s).Process(context.Background(), testMessage(t, events.OrderCreated)); err != nil {
		t.Fatal(err)
	}
	if r.reason != "processing_retries_exhausted" || s.commits != 1 {
		t.Fatalf("%+v %+v", r, s)
	}
}

func TestConsumerOffsetCommitFailureIsReturned(t *testing.T) {
	r := &inboxStub{seen: map[string]bool{}}
	s := &sourceStub{commitError: errors.New("group lost")}
	if err := newTestConsumer(r, s).Process(context.Background(), testMessage(t, events.OrderCreated)); err == nil {
		t.Fatal("commit error lost")
	}
}
