package commands

import (
	"encoding/json"
	"testing"
	"time"

	orders "example.com/food/contracts/orders/v1"
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
