package orders

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestContractCompatibilityAndValidation(t *testing.T) {
	e := Event{NewID(), OrderCreated, 1, 42, 1, "request-42", time.Now().UTC(), Payload{OrderID: 42, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 25000, Currency: "TJS"}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	// A newer producer can add optional fields without breaking v1 consumers.
	body = append(body[:len(body)-1], []byte(`,"optional_future_field":true}`)...)
	got, err := Decode(body)
	if err != nil || got != e {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.EventID = "bad" }, func(e *Event) { e.AggregateVersion = 2 }, func(e *Event) { e.Payload.OrderID = 2 }, func(e *Event) { e.CorrelationID = "\n" }, func(e *Event) { e.Payload.SubtotalMinor = 0 }} {
		bad := e
		change(&bad)
		if !errors.Is(bad.Validate(), ErrInvalid) {
			t.Fatalf("accepted invalid event: %+v", bad)
		}
	}
	e.SchemaVersion = 2
	if !errors.Is(e.Validate(), ErrUnsupported) {
		t.Fatal("accepted unsupported schema")
	}
	if _, err := Decode([]byte(`{`)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestHistoricalCanonicalEventAndPickupContract(t *testing.T) {
	original := `{"event_id":"00000000-0000-0000-0000-000000000001","event_type":"OrderCreated","schema_version":1,"aggregate_id":42,"aggregate_version":1,"correlation_id":"historical","occurred_at":"2026-10-01T00:00:00Z","payload":{"order_id":42,"customer_id":7,"restaurant_id":1,"subtotal_minor":13000,"currency":"TJS"}}`
	e, err := Decode([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(e)
	if err != nil || string(canonical) != original {
		t.Fatal("historical fingerprint changed", string(canonical), err)
	}
	e.Payload.FulfillmentType = "PICKUP"
	e.EventType = OrderPickedUpByCustomer
	e.AggregateVersion = 5
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Payload.FulfillmentType = "DELIVERY"
	if err := e.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatal("delivery customer pickup event", err)
	}
}

func TestPricingContractTotals(t *testing.T) {
	e := Event{EventID: NewID(), EventType: OrderCreated, SchemaVersion: 1, AggregateID: 42, AggregateVersion: 1, CorrelationID: "pricing", OccurredAt: time.Now(), Payload: Payload{FulfillmentType: "DELIVERY", OrderID: 42, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 13000, DeliveryFeeMinor: 1000, TotalMinor: 14000, Currency: "TJS"}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(body)
	if err != nil || decoded.Payload.AmountToCollect() != 14000 {
		t.Fatal(decoded, err)
	}
	for _, change := range []func(*Payload){func(p *Payload) { p.TotalMinor = 13000 }, func(p *Payload) { p.DeliveryFeeMinor = -1 }, func(p *Payload) { p.TotalMinor = 0 }, func(p *Payload) { p.FulfillmentType = "PICKUP" }} {
		bad := e
		change(&bad.Payload)
		if !errors.Is(bad.Validate(), ErrInvalid) {
			t.Fatal(bad)
		}
	}
	historical := e
	historical.Payload.TotalMinor = 0
	historical.Payload.DeliveryFeeMinor = 0
	if historical.Validate() != nil || historical.Payload.AmountToCollect() != 13000 {
		t.Fatal(historical)
	}
}
