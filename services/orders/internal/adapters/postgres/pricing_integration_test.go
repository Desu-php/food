package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/contracts/geo"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestPricedOrderSagaAndFullCollection(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	repo := NewOrders(pool)
	distance := int64(100)
	input, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 10, RestaurantID: 1, Name: "Dish", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, time.Now(), domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Customer address", DeliveryFeeMinor: 1500, RestaurantLocation: &geo.Point{Latitude: 38.56, Longitude: 68.77}, DeliveryLocation: &geo.Point{Latitude: 38.561, Longitude: 68.77}, PricingSnapshot: &domain.PricingSnapshot{SettingsVersion: 2, TariffID: 1, MaxDistanceMeters: 2000, DistanceMeters: &distance, DeliveryFeeMinor: 1500}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := repo.Create(ctx, "priced", strings.Repeat("a", 64), input)
	if err != nil {
		t.Fatal(err)
	}
	id := o.Snapshot().ID
	loaded, err := repo.Get(ctx, 7, id)
	if err != nil || loaded.Snapshot().TotalMinor != 14500 || loaded.Snapshot().DeliveryFeeMinor != 1500 || loaded.Snapshot().DeliveryLocation == nil {
		t.Fatal(loaded, err)
	}
	for _, sql := range []string{`UPDATE orders SET subtotal_minor=subtotal_minor+1 WHERE id=$1`, `UPDATE orders SET delivery_fee_minor=0 WHERE id=$1`, `UPDATE orders SET pricing_snapshot=NULL WHERE id=$1`, `UPDATE orders SET delivery_latitude=0 WHERE id=$1`} {
		if _, err = pool.Exec(ctx, sql, id); err == nil {
			t.Fatal("mutable snapshot", sql)
		}
	}
	staff := application.NewRestaurants(repo, map[int64][]int64{10: {1}})
	if _, err = staff.Change(ctx, 10, 1, id, "accept"); err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err = pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE aggregate_id=$1 AND stream='delivery_commands'`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	command, err := commands.Decode(body)
	if err != nil || command.Payload.AmountToCollectMinor != 14500 || command.Payload.DeliveryLocation == nil {
		t.Fatal(command, err)
	}
	next := func(version int64, kind, status string, confirmation *delivery.Confirmation) {
		t.Helper()
		e := delivery.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: 99, AggregateVersion: version, CorrelationID: "pricing", OccurredAt: time.Now(), Payload: delivery.Payload{OrderID: id, AmountToCollectMinor: 14500, Currency: "TJS", CourierID: 5, Status: status, Confirmation: confirmation}}
		if confirmation != nil {
			e.OccurredAt = confirmation.ConfirmedAt
		}
		if kind == delivery.DeliveryCreated {
			e.Payload.CourierID = 0
		}
		if err := repo.ProcessDelivery(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	next(1, delivery.DeliveryCreated, "PENDING_ASSIGNMENT", nil)
	next(2, delivery.CourierAssigned, "ASSIGNED", nil)
	for _, action := range []string{"prepare", "ready"} {
		if _, err = staff.Change(ctx, 10, 1, id, action); err != nil {
			t.Fatal(err)
		}
	}
	next(3, delivery.DeliveryPickedUp, "PICKED_UP", nil)
	badTime := time.Now()
	bad := delivery.Event{EventID: events.NewID(), EventType: delivery.DeliveryCompleted, SchemaVersion: 1, AggregateID: 99, AggregateVersion: 4, CorrelationID: "pricing", OccurredAt: badTime, Payload: delivery.Payload{OrderID: id, AmountToCollectMinor: 13000, Currency: "TJS", CourierID: 5, Status: "DELIVERED", Confirmation: &delivery.Confirmation{ID: 1, OrderID: id, DeliveryID: 99, AmountMinor: 13000, Currency: "TJS", PaymentMethod: "POS_ON_DELIVERY", ConfirmedBy: 5, ConfirmedAt: badTime}}}
	if err := repo.ProcessDelivery(ctx, bad); !errors.Is(err, domain.ErrPaymentMismatch) {
		t.Fatal("subtotal-only collection accepted", err)
	}
	next(4, delivery.DeliveryCompleted, "DELIVERED", &delivery.Confirmation{ID: 1, OrderID: id, DeliveryID: 99, AmountMinor: 14500, Currency: "TJS", PaymentMethod: "POS_ON_DELIVERY", POSReference: "POS-full", ConfirmedBy: 5, ConfirmedAt: time.Now()})
	done, err := repo.Get(ctx, 7, id)
	if err != nil || done.Snapshot().Status != domain.Delivered || done.Snapshot().PaymentStatus != domain.ReportedCollected || done.Snapshot().TotalMinor != 14500 {
		t.Fatal(done, err)
	}
	replay, found, err := repo.FindCreation(ctx, 7, "priced", strings.Repeat("a", 64))
	if err != nil || !found || replay.Snapshot().Status != domain.WaitingRestaurant || replay.Snapshot().TotalMinor != 14500 {
		t.Fatal(replay, err)
	}
	var event events.Event
	if err = pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE aggregate_id=$1 AND event_type='OrderDelivered'`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &event); err != nil || event.Payload.TotalMinor != 14500 || event.Validate() != nil {
		t.Fatal(event, err)
	}
}
