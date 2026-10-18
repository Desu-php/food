package delivery

import (
	"encoding/json"
	"testing"
	"time"

	orders "github.com/Desu-php/food/contracts/orders/v1"
)

func TestCompletionRequiresMatchingConfirmation(t *testing.T) {
	now := time.Now().UTC()
	p := Confirmation{OrderID: 42, DeliveryID: 1, AmountMinor: 200, Currency: "TJS", PaymentMethod: "POS_ON_DELIVERY", ConfirmedBy: 7, ConfirmedAt: now}
	e := Event{EventID: orders.NewID(), EventType: DeliveryCompleted, SchemaVersion: 1, AggregateID: 1, AggregateVersion: 4, CorrelationID: "test", OccurredAt: now, Payload: Payload{OrderID: 42, CourierID: 7, AmountToCollectMinor: 200, Currency: "TJS", Confirmation: &p}}
	body, _ := json.Marshal(e)
	if _, err := Decode(body); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Confirmation){func(p *Confirmation) { p.AmountMinor++ }, func(p *Confirmation) { p.OrderID++ }, func(p *Confirmation) { p.ConfirmedBy++ }, func(p *Confirmation) { p.PaymentMethod = "BANK" }, func(p *Confirmation) { p.ConfirmedAt = time.Time{} }} {
		bad := p
		change(&bad)
		copy := e
		copy.Payload.Confirmation = &bad
		if copy.Validate() == nil {
			t.Fatal("invalid confirmation accepted", bad)
		}
	}
	e.Payload.Confirmation = nil
	if e.Validate() == nil {
		t.Fatal("missing confirmation accepted")
	}
}
