package postgres

import (
	"context"
	"testing"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	"github.com/Desu-php/food/contracts/geo"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

func TestPricedDeliveryCollectsFullPOSAmount(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	service := application.NewDelivery(repo, []int64{20})
	command := createCommand(42, 1, commands.CreateDelivery)
	command.Payload.AmountToCollectMinor = 26500
	command.Payload.FulfillmentType = "DELIVERY"
	command.Payload.DeliveryAddress = "Customer"
	command.Payload.RestaurantLocation = &geo.Point{Latitude: 38.56, Longitude: 68.77}
	command.Payload.DeliveryLocation = &geo.Point{Latitude: 38.561, Longitude: 68.77}
	if err := repo.ProcessCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	ds, err := repo.List(ctx, 0, 0, 10)
	if err != nil || len(ds) != 1 || ds[0].AmountToCollectMinor != 26500 || ds[0].DeliveryLocation == nil {
		t.Fatal(ds, err)
	}
	d := ds[0]
	c := makeCourier(t, service)
	if _, err = service.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{events.OrderAccepted, events.OrderPreparing, events.OrderReadyForPickup} {
		e := orderEvent(42, int64(i+2), kind)
		e.Payload.TotalMinor = 26500
		e.Payload.DeliveryFeeMinor = 1500
		if err = repo.ProcessOrder(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.Pickup(ctx, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		done, err := service.Complete(ctx, c.ID, d.ID, true, "POS-full")
		if err != nil || done.Confirmation.AmountMinor != 26500 {
			t.Fatal(done, err)
		}
	}
	var confirmations, completed int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM payment_confirmations WHERE delivery_id=$1 AND amount_minor=26500`, d.ID).Scan(&confirmations); err != nil || confirmations != 1 {
		t.Fatal(confirmations, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='DeliveryCompleted' AND (body->'payload'->'confirmation'->>'amount_minor')::bigint=26500`, d.ID).Scan(&completed); err != nil || completed != 1 {
		t.Fatal(completed, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE deliveries SET amount_to_collect_minor=25000 WHERE id=$1`, d.ID); err == nil {
		t.Fatal("mutable collection amount")
	}
}
