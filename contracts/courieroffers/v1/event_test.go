package courieroffers

import (
	ids "github.com/Desu-php/food/contracts/orders/v1"
	"testing"
	"time"
)

func TestOfferContract(t *testing.T) {
	e := Event{EventID: ids.NewID(), EventType: CourierOfferCreated, SchemaVersion: 1, AggregateID: 2, AggregateVersion: 2, CorrelationID: "offer", OccurredAt: time.Now(), Payload: Payload{OfferID: 1, DeliveryID: 2, OrderID: 42, CourierID: 7, Status: "PENDING", ExpiresAt: time.Now().Add(30 * time.Second)}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){func(e *Event) { e.SchemaVersion = 2 }, func(e *Event) { e.Payload.CourierID = 0 }, func(e *Event) { e.Payload.DeliveryID = 3 }, func(e *Event) { e.Payload.Status = "ACCEPTED" }, func(e *Event) { e.CorrelationID = "" }} {
		changed := e
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid offer event accepted")
		}
	}
	e.EventType = CourierOfferClosed
	e.Payload.Status = "EXPIRED"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}
