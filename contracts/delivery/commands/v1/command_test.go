package commands

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	orders "github.com/Desu-php/food/contracts/orders/v1"
)

func TestCommandContract(t *testing.T) {
	c := Command{MessageID: orders.NewID(), CommandType: CreateDelivery, CorrelationID: "test", AggregateID: 42, AggregateVersion: 1, SchemaVersion: 1, OccurredAt: time.Now(), Payload: Payload{OrderID: 42, OrderVersion: 2, AmountToCollectMinor: 25000, Currency: "TJS"}}
	body, _ := json.Marshal(c)
	if _, err := Decode(body); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Command){func(c *Command) { c.CommandType = "OrderAccepted" }, func(c *Command) { c.AggregateVersion = 0 }, func(c *Command) { c.Payload.AmountToCollectMinor = 0 }, func(c *Command) { c.Payload.OrderID = 1 }} {
		bad := c
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid command accepted")
		}
	}
}

func TestHistoricalCanonicalCommandBytesUnchanged(t *testing.T) {
	// Inbox fingerprints are hashes of the canonical struct encoding.
	original := `{"message_id":"15d552cc-a25c-48f1-aa2c-df287c83b322","command_type":"CreateDelivery","correlation_id":"historical","aggregate_id":42,"aggregate_version":1,"schema_version":1,"occurred_at":"2026-10-17T10:00:00Z","payload":{"order_id":42,"order_version":2,"amount_to_collect_minor":25000,"currency":"TJS"}}`
	command, err := Decode([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(command)
	if err != nil || string(encoded) != original {
		t.Fatal(string(encoded), err)
	}
	command.Payload.RestaurantLocation = &geo.Point{Latitude: 38.56, Longitude: 68.77}
	deadline := time.Now()
	command.Payload.DispatchDeadline = &deadline
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(body)
	if err != nil || !geo.Equal(command.Payload.RestaurantLocation, decoded.Payload.RestaurantLocation) {
		t.Fatal(decoded, err)
	}
	command.Payload.RestaurantLocation.Latitude = 91
	if command.Validate() == nil {
		t.Fatal("invalid coordinate accepted")
	}
}
