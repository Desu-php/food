package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	deliveryevents "example.com/food/contracts/delivery/v1"
	events "example.com/food/contracts/orders/v1"
	httpadapter "example.com/food/orders/internal/adapters/http"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

func TestRestaurantHTTPAccessTransitionsAndConcurrency(t *testing.T) {
	pool := integrationPool(t)
	r := NewOrders(pool)
	ctx := context.Background()
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	o, err := r.Create(ctx, "restaurant", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.Snapshot().ID
	h := httpadapter.NewHandler(application.NewOrders(r, nil), pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second, true, svc)
	call := func(user, path string) *httptest.ResponseRecorder {
		method := "POST"
		if path == "/restaurants/1/orders" {
			method = "GET"
		}
		req := httptest.NewRequest(method, path, nil)
		if user != "" {
			req.Header.Set("X-Dev-User-ID", user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	path := fmt.Sprintf("/restaurants/1/orders/%d/", id)
	for user, want := range map[string]int{"": 401, "99": 403} {
		if w := call(user, path+"accept"); w.Code != want {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := call("10", fmt.Sprintf("/restaurants/2/orders/%d/accept", id)); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("10", "/restaurants/1/orders"); w.Code != 200 || !strings.Contains(w.Body.String(), "WAITING_RESTAURANT") {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("10", path+"prepare"); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	var wg sync.WaitGroup
	var success, conflicts atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Change(ctx, 10, 1, id, "accept")
			if err == nil {
				success.Add(1)
			} else if errors.Is(err, domain.ErrInvalidTransition) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || conflicts.Load() != 7 {
		t.Fatal(success.Load(), conflicts.Load())
	}
	if w := call("10", path+"prepare"); w.Code != 409 {
		t.Fatal("preparing without courier", w.Code, w.Body)
	}
	bindAndAssign(t, r, id)
	for _, action := range []string{"prepare", "ready"} {
		if w := call("10", path+action); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, id).Scan(&count); err != nil || count != 7 {
		t.Fatal(count, err)
	}
	rejected, err := r.Create(ctx, "reject", strings.Repeat("b", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	path = fmt.Sprintf("/restaurants/1/orders/%d/", rejected.Snapshot().ID)
	if w := call("10", path+"reject"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("10", path+"prepare"); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("10", "/restaurants/1/orders"); w.Code != 200 || strings.Contains(w.Body.String(), "CANCELLED") {
		t.Fatal(w.Code, w.Body)
	}
	// Business mutation and Outbox are indivisible.
	blocked, err := r.Create(ctx, "blocked", strings.Repeat("c", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_accept CHECK(event_type<>'OrderAccepted') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Change(ctx, 10, 1, blocked.Snapshot().ID, "accept"); err == nil {
		t.Fatal("accept persisted without Outbox")
	}
	current, err := r.Get(ctx, 7, blocked.Snapshot().ID)
	if err != nil || current.Snapshot().Status != domain.WaitingRestaurant {
		t.Fatal(current, err)
	}
}
func TestDeliveryInboxAtomicOrderAndPayment(t *testing.T) {
	pool := integrationPool(t)
	r := NewOrders(pool)
	ctx := context.Background()
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	o, err := r.Create(ctx, "delivery", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.Snapshot().ID
	for _, action := range []string{"accept"} {
		if _, err = svc.Change(ctx, 10, 1, id, action); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	e := deliveryevents.Event{EventID: events.NewID(), EventType: deliveryevents.DeliveryCreated, SchemaVersion: 1, AggregateID: 99, AggregateVersion: 1, CorrelationID: "delivery", OccurredAt: now, Payload: deliveryevents.Payload{OrderID: id, AmountToCollectMinor: 13100, Currency: "TJS"}}
	for _, kind := range []string{deliveryevents.DeliveryCreated, deliveryevents.CourierAssigned, deliveryevents.DeliveryPickedUp} {
		if kind == deliveryevents.DeliveryPickedUp {
			for _, action := range []string{"prepare", "ready"} {
				if _, err = svc.Change(ctx, 10, 1, id, action); err != nil {
					t.Fatal(err)
				}
			}
		}
		e.EventType = kind
		if kind != deliveryevents.DeliveryCreated {
			e.AggregateVersion++
			e.EventID = events.NewID()
			e.Payload.CourierID = 5
		}
		if err = r.ProcessDelivery(ctx, e); err != nil {
			t.Fatal(kind, err)
		}
		if err = r.ProcessDelivery(ctx, e); err != nil {
			t.Fatal("duplicate", err)
		}
	}
	e.EventType = deliveryevents.DeliveryCompleted
	e.AggregateVersion++
	e.EventID = events.NewID()
	e.Payload.Confirmation = &deliveryevents.Confirmation{OrderID: id, DeliveryID: 99, AmountMinor: 13100, Currency: "TJS", PaymentMethod: "POS_ON_DELIVERY", POSReference: "POS-99", ConfirmedBy: 5, ConfirmedAt: now}
	bad := e
	bad.Payload.Confirmation = nil
	if err = r.ProcessDelivery(ctx, bad); err == nil {
		t.Fatal("completion without payment accepted")
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_delivered CHECK(event_type<>'OrderDelivered')`); err != nil {
		t.Fatal(err)
	}
	if err = r.ProcessDelivery(ctx, e); err == nil {
		t.Fatal("partial completion persisted")
	}
	current, err := r.Get(ctx, 7, id)
	if err != nil || current.Snapshot().Status != domain.OnTheWay || current.Snapshot().PaymentStatus != domain.Unpaid {
		t.Fatal(current, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM delivery_inbox`).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_delivered`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.ProcessDelivery(ctx, e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	current, err = r.Get(ctx, 7, id)
	if err != nil || current.Snapshot().Status != domain.Delivered || current.Snapshot().PaymentStatus != domain.ReportedCollected || current.Snapshot().Version != 8 {
		t.Fatal(current, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='OrderDelivered'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	bad = e
	bad.EventID = events.NewID()
	if err = r.ProcessDelivery(ctx, bad); !errors.Is(err, application.ErrEventConflict) {
		t.Fatal(err)
	}
}
func TestFulfillmentMigrationPreservesHistoricalData(t *testing.T) {
	pool := integrationPool(t, "000001_orders", "000002_outbox")
	ctx := context.Background()
	// Populate the original schema without using the new application defaults.
	var id int64
	now := time.Now().UTC()
	err := pool.QueryRow(ctx, `INSERT INTO orders(customer_id,restaurant_id,status,currency,subtotal_minor,created_at,updated_at) VALUES(7,1,'PENDING_PAYMENT','TJS',100,$1,$1) RETURNING id`, now).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	var item int64
	if err = pool.QueryRow(ctx, `INSERT INTO order_items(order_id,menu_item_id,name,quantity,unit_price_minor,total_price_minor) VALUES($1,1,'old',1,100,100) RETURNING id`, id).Scan(&item); err != nil {
		t.Fatal(err)
	}
	snapshot := domain.State{ID: id, CustomerID: 7, RestaurantID: 1, Status: domain.PendingPayment, Currency: "TJS", SubtotalMinor: 100, CreatedAt: now, UpdatedAt: now, Items: []domain.OrderItem{{ID: item, OrderID: id, MenuItemID: 1, Name: "old", Quantity: 1, UnitPriceMinor: 100, TotalPriceMinor: 100}}}
	body, _ := json.Marshal(snapshot)
	if _, err = pool.Exec(ctx, `INSERT INTO idempotency_keys(customer_id,key,request_hash,order_id,response) VALUES(7,'old',$1,$2,$3)`, strings.Repeat("a", 64), id, body); err != nil {
		t.Fatal(err)
	}
	event := events.Event{EventID: events.NewID(), EventType: events.OrderCreated, SchemaVersion: 1, AggregateID: id, AggregateVersion: 1, CorrelationID: "historical", OccurredAt: now, Payload: events.Payload{OrderID: id, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 100, Currency: "TJS"}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if err = insertEvent(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down", "up"} {
		sql, err := os.ReadFile("../../../migrations/000003_fulfillment." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(direction, err)
		}
	}
	sagaSQL, err := os.ReadFile("../../../migrations/000004_saga.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sagaSQL)); err != nil {
		t.Fatal(err)
	}
	pickupSQL, err := os.ReadFile("../../../migrations/000005_pickup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(pickupSQL)); err != nil {
		t.Fatal(err)
	}
	current, err := NewOrders(pool).Get(ctx, 7, id)
	if err != nil || current.Snapshot().Status != domain.WaitingRestaurant {
		t.Fatal(current, err)
	}
	historical, found, err := NewOrders(pool).FindCreation(ctx, 7, "old", strings.Repeat("a", 64))
	if err != nil || !found || historical.Snapshot().Status != domain.PendingPayment {
		t.Fatal(historical, found, err)
	}
}

func TestFulfillmentRollbackRefusesToDestroyAcceptedOrders(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	r := NewOrders(pool)
	o, err := r.Create(ctx, "rollback-guard", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	if _, err = svc.Change(ctx, 10, 1, o.Snapshot().ID, "accept"); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../../../migrations/000003_fulfillment.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sql)); err == nil {
		t.Fatal("rollback erased fulfillment")
	}
	got, err := r.Get(ctx, 7, o.Snapshot().ID)
	if err != nil || got.Snapshot().Status != domain.Accepted {
		t.Fatal(got, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='OrderAccepted'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func bindAndAssign(t *testing.T, r *Orders, id int64) {
	t.Helper()
	ctx := context.Background()
	o, err := r.Get(ctx, 7, id)
	if err != nil {
		t.Fatal(err)
	}
	e := deliveryevents.Event{EventID: events.NewID(), EventType: deliveryevents.DeliveryCreated, SchemaVersion: 1, AggregateID: id + 100, AggregateVersion: 1, CorrelationID: "bind", OccurredAt: time.Now().UTC(), Payload: deliveryevents.Payload{OrderID: id, AmountToCollectMinor: o.Snapshot().SubtotalMinor, Currency: "TJS"}}
	if err = r.ProcessDelivery(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.EventID = events.NewID()
	e.EventType = deliveryevents.CourierAssigned
	e.AggregateVersion = 2
	e.Payload.CourierID = 5
	if err = r.ProcessDelivery(ctx, e); err != nil {
		t.Fatal(err)
	}
}
