package postgres

import (
	"context"
	"errors"
	"testing"

	commands "example.com/food/contracts/delivery/commands/v1"
	events "example.com/food/contracts/orders/v1"
	"example.com/food/delivery/internal/application"
)

func TestPickupEventsHaveNoDeliveryEffects(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	for i, kind := range []string{events.OrderCreated, events.OrderAccepted, events.OrderPreparing, events.OrderReadyForPickup, events.OrderPickedUpByCustomer} {
		e := orderEvent(42, int64(i+1), kind)
		e.Payload.FulfillmentType = "PICKUP"
		if err := r.ProcessOrder(ctx, e); err != nil {
			t.Fatal(kind, err)
		}
		if err := r.ProcessOrder(ctx, e); err != nil {
			t.Fatal("duplicate", err)
		}
	}
	for _, table := range []string{"deliveries", "outbox_events", "payment_confirmations", "delivery_requests"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	var version int64
	var preparing, ready bool
	if err := pool.QueryRow(ctx, `SELECT version,preparation_started,ready FROM order_streams WHERE order_id=42`).Scan(&version, &preparing, &ready); err != nil || version != 5 || preparing || ready {
		t.Fatal("delivery projection affected", version, err)
	}
}
func TestDeliveryCommandKeepsAddressAndRejectsPickup(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	c := createCommand(42, 1, commands.CreateDelivery)
	c.Payload.FulfillmentType = "DELIVERY"
	c.Payload.DeliveryAddress = "Customer location"
	if err := r.ProcessCommand(ctx, c); err != nil {
		t.Fatal(err)
	}
	d, err := r.Get(ctx, 0, 1)
	if err != nil || d.DeliveryAddress != "Customer location" {
		t.Fatal(d, err)
	}
	next := createCommand(42, 2, commands.CreateDelivery)
	next.Payload.FulfillmentType = "DELIVERY"
	next.Payload.DeliveryAddress = "Other location"
	if err := r.ProcessCommand(ctx, next); !errors.Is(err, application.ErrEventConflict) {
		t.Fatal("location changed", err)
	}
	next = createCommand(43, 1, commands.CreateDelivery)
	next.Payload.FulfillmentType = "PICKUP"
	if err := r.ProcessCommand(ctx, next); !errors.Is(err, commands.ErrInvalid) {
		t.Fatal("pickup command accepted", err)
	}
}
