package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestSagaMigrationReconcilesLegacyPickedUpOrder(t *testing.T) {
	pool := integrationPool(t, "000001_orders", "000002_outbox", "000003_fulfillment")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO orders(customer_id,restaurant_id,status,currency,subtotal_minor,version,created_at,updated_at) VALUES(7,1,'READY_FOR_PICKUP','TJS',25000,4,now(),now());
 INSERT INTO order_items(order_id,menu_item_id,name,quantity,unit_price_minor,total_price_minor) VALUES(1,1,'Legacy dish',1,25000,25000);
 INSERT INTO order_deliveries(order_id,delivery_id,version) VALUES(1,99,2);`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../migrations/000004_saga.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	pickupSQL, err := os.ReadFile("../../../migrations/000005_pickup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(pickupSQL)); err != nil {
		t.Fatal(err)
	}
	applyDispatchMigration(t, pool)
	pushSQL, err := os.ReadFile("../../../migrations/000009_push_events.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(pushSQL)); err != nil {
		t.Fatal(err)
	}
	r := NewOrders(pool)
	s, err := r.Saga(ctx, 1, 1)
	if err != nil || s.State != domain.SagaCreatingDelivery || s.Reason != "legacy_reconciliation" || s.DeliveryVersion != 2 {
		t.Fatal(s, err)
	}
	// A remote outage may outlive the command deadline; reconciliation permission
	// is persisted separately from diagnostic reasons and survives that escalation.
	scheduled, found, err := r.StepSaga(ctx, time.Now().Add(time.Second))
	if err != nil || !found {
		t.Fatal(scheduled, found, err)
	}
	escalated, found, err := r.StepSaga(ctx, scheduled.Deadline.Add(time.Millisecond))
	if err != nil || !found || escalated.State != domain.SagaNeedsAttention || !escalated.LegacyReconciliation {
		t.Fatal(escalated, found, err)
	}
	// Pickup was already published before the coordinated upgrade; no synthetic assignment is invented.
	e := delivery.Event{EventID: events.NewID(), EventType: delivery.DeliveryPickedUp, SchemaVersion: 1, AggregateID: 99, AggregateVersion: 3, CorrelationID: "legacy", OccurredAt: time.Now().UTC(), Payload: delivery.Payload{OrderID: 1, CourierID: 7, AmountToCollectMinor: 25000, Currency: "TJS"}}
	if err = r.ProcessDelivery(ctx, e); err != nil {
		t.Fatal(err)
	}
	o, err := r.Get(ctx, 7, 1)
	if err != nil || o.Snapshot().Status != domain.OnTheWay || o.Snapshot().CourierID != 7 || o.Snapshot().Version != 5 {
		t.Fatal(o, err)
	}
	s, err = r.Saga(ctx, 1, 1)
	if err != nil || s.State != domain.SagaOnTheWay {
		t.Fatal(s, err)
	}
}
func TestEmptySagaMigrationRoundTrip(t *testing.T) {
	pool := integrationPool(t, "000001_orders", "000002_outbox", "000003_fulfillment", "000004_saga")
	ctx := context.Background()
	for _, direction := range []string{"down", "up"} {
		body, err := os.ReadFile("../../../migrations/000004_saga." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(direction, err)
		}
	}
	pickupSQL, err := os.ReadFile("../../../migrations/000005_pickup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(pickupSQL)); err != nil {
		t.Fatal(err)
	}
	applyDispatchMigration(t, pool)
	pushSQL, err := os.ReadFile("../../../migrations/000009_push_events.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(pushSQL)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOrders(pool).Create(ctx, "roundtrip", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", newOrder(t)); err != nil {
		t.Fatal(err)
	}
}

func TestSagaDowngradeKeepsHistoryAfterOutboxRetention(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	r := NewOrders(pool)
	o, err := r.Create(ctx, "retention-guard", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	if _, err = svc.Change(ctx, 10, 1, o.Snapshot().ID, "accept"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE outbox_events SET status='PUBLISHED',published_at=now()-interval '10 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := NewOutbox(pool).Prune(ctx, 24*time.Hour); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	body, err := os.ReadFile("../../../migrations/000004_saga.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(body)); err == nil {
		t.Fatal("downgrade erased Saga after Outbox cleanup")
	}
	s, err := r.Saga(ctx, 1, o.Snapshot().ID)
	if err != nil || s.CommandSequence != 1 || s.State != domain.SagaCreatingDelivery {
		t.Fatal(s, err)
	}
}
