package postgres

import (
	"context"
	"encoding/json"
	"testing"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/application"
)

func TestPickupReadyAndCompletionNotifications(t *testing.T) {
	pool := integrationPool(t)
	svc := application.NewNotifications(NewInbox(pool))
	ctx := context.Background()
	for i, kind := range []string{events.OrderReadyForPickup, events.OrderPickedUpByCustomer} {
		m := message(t, events.OrderCreated, 42)
		var e events.Event
		if err := json.Unmarshal(m.Body, &e); err != nil {
			t.Fatal(err)
		}
		e.EventType = kind
		e.AggregateVersion = int64(4 + i)
		e.Payload.FulfillmentType = "PICKUP"
		var err error
		m.Body, err = json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, duplicate, err := svc.Handle(ctx, m); err != nil || duplicate {
			t.Fatal(kind, err)
		}
		if _, duplicate, err := svc.Handle(ctx, m); err != nil || !duplicate {
			t.Fatal("duplicate notification", err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE fulfillment_type='PICKUP'`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE status='PENDING' AND fulfillment_type='PICKUP'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatal(pending, err)
	}
}
