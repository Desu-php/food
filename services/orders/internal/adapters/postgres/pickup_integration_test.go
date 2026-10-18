package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/orders/internal/adapters/catalog"
	httpadapter "example.com/food/orders/internal/adapters/http"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

func createPickup(t *testing.T, r *Orders, key string) domain.Order {
	t.Helper()
	o, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 100, RestaurantID: 1, Name: "Dish", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, time.Now(), domain.Fulfillment{Type: domain.Pickup, PickupAddress: "Restaurant location"})
	if err != nil {
		t.Fatal(err)
	}
	o, err = r.Create(context.Background(), key, strings.Repeat("a", 64), o)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func readyPickup(t *testing.T, r *Orders, id int64) {
	t.Helper()
	s := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	for _, action := range []string{"accept", "prepare", "ready"} {
		if _, err := s.Change(context.Background(), 10, 1, id, action); err != nil {
			t.Fatal(action, err)
		}
	}
}
func pickupCounts(t *testing.T, pool *pgxpool.Pool, id int64, payments, outbox int) {
	t.Helper()
	ctx := context.Background()
	for _, check := range []struct {
		query string
		want  int
	}{{`SELECT count(*) FROM pickup_payment_confirmations WHERE order_id=$1`, payments}, {`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='OrderPickedUpByCustomer'`, outbox}, {`SELECT count(*) FROM order_sagas WHERE order_id=$1`, 0}, {`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND stream='delivery_commands'`, 0}} {
		var n int
		if err := pool.QueryRow(ctx, check.query, id).Scan(&n); err != nil || n != check.want {
			t.Fatalf("%s: %d want %d: %v", check.query, n, check.want, err)
		}
	}
}

func TestPickupCompletionAtomicConcurrentAndIdempotent(t *testing.T) {
	pool := integrationPool(t)
	r := NewOrders(pool)
	ctx := context.Background()
	o := createPickup(t, r, "pickup")
	id := o.Snapshot().ID
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}, 11: {2}, 12: {1}})
	customer := application.NewOrders(r, nil)
	input := application.PickupCompletion{Code: strings.Repeat("a", 32), PaymentConfirmed: true, ReceiptReference: "POS-42"}
	if _, err := svc.CompletePickup(ctx, 11, 1, id, input); !errors.Is(err, application.ErrForbidden) {
		t.Fatal("foreign restaurant", err)
	}
	if _, err := svc.CompletePickup(ctx, 10, 1, id, input); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal("not ready", err)
	}
	readyPickup(t, r, id)
	if _, err := customer.IssuePickupCode(ctx, 8, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("customer isolation", err)
	}
	code, err := customer.IssuePickupCode(ctx, 7, id)
	if err != nil {
		t.Fatal(err)
	}
	input.Code = code.Code
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT code_hash FROM pickup_codes WHERE order_id=$1`, id).Scan(&stored); err != nil || bytes.Contains(stored, []byte(code.Code)) {
		t.Fatal("code storage", err)
	}
	if _, err := svc.VerifyPickup(ctx, 11, 1, id, input.Code); !errors.Is(err, application.ErrForbidden) {
		t.Fatal(err)
	}
	verified, err := svc.VerifyPickup(ctx, 10, 1, id, input.Code)
	if err != nil || verified.Snapshot().Status != domain.ReadyForPickup || verified.Snapshot().PaymentStatus != domain.Unpaid {
		t.Fatal("verification changed order", err)
	}
	pickupCounts(t, pool, id, 0, 0)
	wrong := input
	wrong.Code = strings.Repeat("0", 32)
	if _, err := svc.CompletePickup(ctx, 10, 1, id, wrong); !errors.Is(err, application.ErrPickupCode) {
		t.Fatal(err)
	}
	unpaid := input
	unpaid.PaymentConfirmed = false
	if _, err := svc.CompletePickup(ctx, 10, 1, id, unpaid); !errors.Is(err, domain.ErrPaymentMismatch) {
		t.Fatal(err)
	}
	// Force the last business write to fail. Neither payment nor order may commit.
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_pickup CHECK(event_type<>'OrderPickedUpByCustomer')`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompletePickup(ctx, 10, 1, id, input); err == nil {
		t.Fatal("partial completion accepted")
	}
	pickupCounts(t, pool, id, 0, 0)
	current, err := r.Get(ctx, 7, id)
	if err != nil || current.Snapshot().Status != domain.ReadyForPickup || current.Snapshot().PaymentStatus != domain.Unpaid {
		t.Fatal(current, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_pickup`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.CompletePickup(ctx, 10, 1, id, input)
			if err != nil || got.Snapshot().Status != domain.PickedUpByCustomer {
				t.Errorf("complete: %v", err)
			}
		}()
	}
	wg.Wait()
	pickupCounts(t, pool, id, 1, 1)
	var by, amount int64
	var currency, method, receipt string
	var at time.Time
	if err := pool.QueryRow(ctx, `SELECT confirmed_by,amount_minor,currency,payment_method,confirmed_at,receipt_reference FROM pickup_payment_confirmations WHERE order_id=$1`, id).Scan(&by, &amount, &currency, &method, &at, &receipt); err != nil || by != 10 || amount != 13000 || currency != "TJS" || method != domain.POSAtPickup || receipt != "POS-42" || at.IsZero() {
		t.Fatal("audit", by, amount, err)
	}
	// Simulate restart and retry after the token expired: it is a replay, not a second handoff.
	if _, err := pool.Exec(ctx, `UPDATE pickup_codes SET expires_at=now()-interval '1 second' WHERE order_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	svc = application.NewRestaurants(NewOrders(pool), map[int64][]int64{10: {1}, 12: {1}})
	if _, err := svc.CompletePickup(ctx, 10, 1, id, input); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompletePickup(ctx, 12, 1, id, input); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatal("changed actor", err)
	}
	changed := input
	changed.ReceiptReference = "different"
	if _, err := svc.CompletePickup(ctx, 10, 1, id, changed); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatal("changed receipt", err)
	}
	if _, err := customer.IssuePickupCode(ctx, 7, id); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal("terminal token rotated", err)
	}
	original, found, err := r.FindCreation(ctx, 7, "pickup", strings.Repeat("a", 64))
	if err != nil || !found || original.Snapshot().Status != domain.WaitingRestaurant {
		t.Fatal("creation response changed", err)
	}
	if _, found, err := r.StepSaga(ctx, time.Now().Add(48*time.Hour)); err != nil || found {
		t.Fatal("pickup scheduled saga", found, err)
	}
	var body []byte
	if err := pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE aggregate_id=$1 AND event_type='OrderPickedUpByCustomer'`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	event, err := events.Decode(body)
	if err != nil || event.Payload.FulfillmentType != "PICKUP" || bytes.Contains(body, []byte(input.Code)) {
		t.Fatal("event", event, err)
	}
}

func TestPickupRateLimitRotationExpiryAndCancellation(t *testing.T) {
	pool := integrationPool(t)
	r := NewOrders(pool)
	ctx := context.Background()
	o := createPickup(t, r, "security")
	id := o.Snapshot().ID
	readyPickup(t, r, id)
	customer := application.NewOrders(r, nil)
	staff := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	code, err := customer.IssuePickupCode(ctx, 7, id)
	if err != nil {
		t.Fatal(err)
	}
	wrong := application.PickupCompletion{Code: strings.Repeat("0", 32), PaymentConfirmed: true}
	for range 5 {
		if _, err := staff.CompletePickup(ctx, 10, 1, id, wrong); !errors.Is(err, application.ErrPickupCode) {
			t.Fatal(err)
		}
	}
	correct := wrong
	correct.Code = code.Code
	if _, err := staff.CompletePickup(ctx, 10, 1, id, correct); !errors.Is(err, application.ErrPickupRateLimited) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pickup_codes SET blocked_until=now()-interval '1 second' WHERE order_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := staff.CompletePickup(ctx, 10, 1, id, wrong); !errors.Is(err, application.ErrPickupCode) {
		t.Fatal("cooldown did not reset attempts", err)
	}
	rotated, err := customer.IssuePickupCode(ctx, 7, id)
	if err != nil || rotated.Code == code.Code {
		t.Fatal("rotation", err)
	}
	if _, err := staff.CompletePickup(ctx, 10, 1, id, correct); !errors.Is(err, application.ErrPickupCode) {
		t.Fatal("old token valid", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE pickup_codes SET expires_at=now()-interval '1 second' WHERE order_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	correct.Code = rotated.Code
	if _, err := staff.CompletePickup(ctx, 10, 1, id, correct); !errors.Is(err, application.ErrPickupCode) {
		t.Fatal("expired token valid", err)
	}
	pickupCounts(t, pool, id, 0, 0)
	for _, accept := range []bool{false, true} {
		o := createPickup(t, r, fmt.Sprint("cancel-", accept))
		id := o.Snapshot().ID
		if accept {
			if _, err := staff.Change(ctx, 10, 1, id, "accept"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := customer.CancelOrder(ctx, 7, id); err != nil {
			t.Fatal(err)
		}
		pickupCounts(t, pool, id, 0, 0)
	}
}

func TestPickupHTTPWithCatalog(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"id":1,"address":"Restaurant-owned location"}`)
			return
		}
		io.WriteString(w, `{"restaurant_id":1,"pickup_address":"Restaurant-owned location","currency":"TJS","items":[{"menu_item_id":100,"restaurant_id":1,"name":"Dish","quantity":2,"unit_price_minor":6500,"currency":"TJS"}]}`)
	}))
	defer catalogServer.Close()
	client, err := catalog.NewClient(catalogServer.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	repo := NewOrders(pool)
	loggerOutput := new(bytes.Buffer)
	h := httpadapter.NewHandler(application.NewOrders(repo, client), pool.Ping, slog.New(slog.NewJSONHandler(loggerOutput, nil)), time.Second, true, application.NewRestaurants(repo, map[int64][]int64{10: {1}, 11: {2}}))
	call := func(method, path, body string, staff bool, actor string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "http-pickup")
		if staff {
			req.Header.Set("X-Dev-User-ID", actor)
		} else {
			req.Header.Set("X-Dev-Customer-ID", actor)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	body := `{"restaurant_id":1,"fulfillment_type":"PICKUP","expected_subtotal_minor":13000,"items":[{"menu_item_id":100,"quantity":2}]}`
	created := call("POST", "/orders", body, false, "7")
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	var snapshot struct {
		ID            int64  `json:"id"`
		PickupAddress string `json:"pickup_address"`
		DeliveryFee   int64  `json:"delivery_fee_minor"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.PickupAddress != "Restaurant-owned location" || snapshot.DeliveryFee != 0 {
		t.Fatal(snapshot)
	}
	id := snapshot.ID
	readyPickup(t, repo, id)
	codeReply := call("POST", fmt.Sprintf("/orders/%d/pickup-code", id), "", false, "7")
	if codeReply.Code != 200 || codeReply.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(codeReply.Code, codeReply.Body)
	}
	var code application.PickupCode
	if err := json.Unmarshal(codeReply.Body.Bytes(), &code); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/restaurants/1/orders/%d/complete-pickup", id)
	if w := call("POST", fmt.Sprintf("/restaurants/1/orders/%d/verify-pickup", id), fmt.Sprintf(`{"pickup_code":%q}`, code.Code), true, "10"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	completion := fmt.Sprintf(`{"pickup_code":%q,"payment_confirmed":true,"receipt_reference":"POS-http"}`, code.Code)
	if w := call("POST", path, completion, true, "11"); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	first := call("POST", path, completion, true, "10")
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	if w := call("POST", path, completion, true, "10"); w.Code != 200 || w.Body.String() != first.Body.String() {
		t.Fatal("handoff replay changed", w.Code, w.Body)
	}
	if loggerOutput.Len() == 0 || strings.Contains(loggerOutput.String(), code.Code) {
		t.Fatal("pickup code leaked into logs")
	}
	if w := call("POST", "/orders", body, false, "7"); w.Code != 201 || w.Body.String() != created.Body.String() {
		t.Fatal("immutable replay", w.Code, w.Body)
	}
	pickupCounts(t, pool, id, 1, 1)
	if _, err := repo.Get(ctx, 7, id); err != nil {
		t.Fatal(err)
	}
}

func TestPickupMigrationPreservesHistoricalFactsAndGuardsDowngrade(t *testing.T) {
	pool := integrationPool(t, "000001_orders", "000002_outbox", "000003_fulfillment", "000004_saga")
	ctx := context.Background()
	// Old service has no fulfillment/address fields. Snapshot and event bytes stay unchanged.
	if _, err := pool.Exec(ctx, `INSERT INTO orders(customer_id,restaurant_id,status,currency,subtotal_minor,version,created_at,updated_at) VALUES(7,1,'DELIVERED','TJS',100,6,now(),now());
 INSERT INTO order_items(order_id,menu_item_id,name,quantity,unit_price_minor,total_price_minor) VALUES(1,1,'Legacy',1,100,100);`); err == nil {
		t.Fatal("old payment invariant absent")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO orders(customer_id,restaurant_id,status,currency,subtotal_minor,version,payment_status,created_at,updated_at) VALUES(7,1,'DELIVERED','TJS',100,6,'REPORTED_COLLECTED',now(),now());`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM orders`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO order_items(order_id,menu_item_id,name,quantity,unit_price_minor,total_price_minor) VALUES($1,1,'Legacy',1,100,100)`, id); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := pool.QueryRow(ctx, `SELECT row_to_json(o)::text::bytea FROM orders o WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../../migrations/000005_pickup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	applyDispatchMigration(t, pool)
	legacy, err := NewOrders(pool).Get(ctx, 7, id)
	if err != nil {
		t.Fatal(err)
	}
	s := legacy.Snapshot()
	if s.Status != domain.Delivered || s.PaymentStatus != domain.ReportedCollected || s.FulfillmentType != domain.Delivery || !s.LegacyDeliveryAddress || s.DeliveryAddress != "" || s.Version != 6 {
		t.Fatal(s)
	}
	var old, newFields map[string]json.RawMessage
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := pool.QueryRow(ctx, `SELECT row_to_json(o)::text::bytea FROM orders o WHERE id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &newFields); err != nil {
		t.Fatal(err)
	}
	for key, value := range old {
		if !bytes.Equal(value, newFields[key]) {
			t.Fatal("historical fact changed", key)
		}
	}
	down, err := os.ReadFile("../../../migrations/000005_pickup.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	o := createPickup(t, NewOrders(pool), "no-downgrade")
	if _, err := pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("pickup history erased")
	}
	pickupCounts(t, pool, o.Snapshot().ID, 0, 0)
}
