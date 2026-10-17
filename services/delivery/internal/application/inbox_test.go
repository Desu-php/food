package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	commands "example.com/food/contracts/delivery/commands/v1"
	events "example.com/food/contracts/orders/v1"
)

type source struct {
	commits int
	err     error
}

func (s *source) Poll(context.Context) (Message, error) { return Message{}, errors.New("not used") }
func (s *source) Commit(context.Context, Message) error { s.commits++; return s.err }

type inbox struct {
	calls int
	err   error
}

func (i *inbox) ProcessOrder(context.Context, events.Event) error       { i.calls++; return i.err }
func (i *inbox) ProcessCommand(context.Context, commands.Command) error { i.calls++; return i.err }
func TestConsumerRetainsOffsetOnFailure(t *testing.T) {
	e := events.Event{EventID: events.NewID(), EventType: events.OrderAccepted, SchemaVersion: 1, AggregateID: 42, AggregateVersion: 2, CorrelationID: "test", OccurredAt: time.Now(), Payload: events.Payload{OrderID: 42, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 200, Currency: "TJS"}}
	body, _ := json.Marshal(e)
	m := Message{Topic: events.Topic, Key: []byte("42"), Body: body}
	s := &source{}
	r := &inbox{err: errors.New("database unavailable")}
	c := NewConsumer(s, r)
	if c.Process(context.Background(), m) == nil || s.commits != 0 {
		t.Fatal("failed storage acknowledged")
	}
	r.err = nil
	s.err = errors.New("commit failed")
	if c.Process(context.Background(), m) == nil {
		t.Fatal("failed offset commit ignored")
	}
	s.err = nil
	if err := c.Process(context.Background(), m); err != nil || r.calls != 3 {
		t.Fatal("replay failed", err)
	}
	m.Key = []byte("99")
	if c.Process(context.Background(), m) == nil || s.commits != 2 {
		t.Fatal("invalid key acknowledged")
	}
}
