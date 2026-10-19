package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	deliveryevents "github.com/Desu-php/food/contracts/delivery/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func integrationPool(t *testing.T, migrations ...string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("delivery_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if len(migrations) == 0 {
		migrations = []string{"000001_delivery", "000002_saga", "000003_delivery_address", "000004_consumer_quarantine", "000005_automatic_dispatch", "000006_courier_offers", "000007_tracking", "000008_delivery_coordinates", "000009_push_events"}
	}
	for _, name := range migrations {
		body, err := os.ReadFile("../../../migrations/" + name + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
func orderEvent(order, version int64, kind string) events.Event {
	return events.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: order, AggregateVersion: version, CorrelationID: "test", OccurredAt: time.Now().UTC(), Payload: events.Payload{OrderID: order, CustomerID: 7, RestaurantID: 1, SubtotalMinor: 25000, Currency: "TJS"}}
}
func accepted(t *testing.T, r *Repository, order int64) domain.Delivery {
	t.Helper()
	if err := r.ProcessOrder(context.Background(), orderEvent(order, 2, events.OrderAccepted)); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessCommand(context.Background(), createCommand(order, 1, commands.CreateDelivery)); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := r.pool.QueryRow(context.Background(), `SELECT id FROM deliveries WHERE order_id=$1`, order).Scan(&id); err != nil {
		t.Fatal(err)
	}
	d, err := r.Get(context.Background(), 0, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func makeCourier(t *testing.T, s *application.Delivery) domain.Courier {
	t.Helper()
	c, err := s.CreateCourier(context.Background(), 20, "Курьер")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func ready(t *testing.T, r *Repository, order int64) {
	t.Helper()
	for _, e := range []events.Event{orderEvent(order, 3, events.OrderPreparing), orderEvent(order, 4, events.OrderReadyForPickup)} {
		if err := r.ProcessOrder(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDeliveryAtomicPOSAndIdempotency(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	d := accepted(t, r, 42)
	c := makeCourier(t, s)
	if _, err := s.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pickup(ctx, c.ID, d.ID); !errors.Is(err, domain.ErrTransition) {
		t.Fatal("picked up unready order", err)
	}
	ready(t, r, 42)
	if _, err := s.Pickup(ctx, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, c.ID, d.ID, false, ""); !errors.Is(err, domain.ErrPayment) {
		t.Fatal(err)
	}
	// An Outbox write failure must undo completion, confirmation, and courier release.
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_completed CHECK(event_type<>'DeliveryCompleted')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, c.ID, d.ID, true, "POS-42"); err == nil {
		t.Fatal("completion survived Outbox failure")
	}
	var count int
	var status string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_confirmations`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM couriers WHERE id=$1`, c.ID).Scan(&status); err != nil || status != "BUSY" {
		t.Fatal(status, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.Status != domain.PickedUp {
		t.Fatal(got, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_completed`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Complete(ctx, c.ID, d.ID, true, "POS-42")
			if err != nil || result.Confirmation == nil || result.Confirmation.AmountMinor != 25000 {
				t.Error(result, err)
			}
		}()
	}
	wg.Wait()
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM payment_confirmations`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='DeliveryCompleted'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var body []byte
	if err = pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE event_type='DeliveryCompleted'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	event, err := deliveryevents.Decode(body)
	if err != nil || event.Payload.Confirmation.AmountMinor != 25000 || event.Payload.Confirmation.ConfirmedBy != c.ID {
		t.Fatal(event, err)
	}
	if _, err = s.Complete(ctx, c.ID+1, d.ID, true, "POS-42"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.Complete(ctx, c.ID, d.ID, true, "different"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	d2 := accepted(t, r, 43)
	if _, err = s.Assign(ctx, 20, d2.ID, c.ID); err != nil {
		t.Fatal("courier not released", err)
	}
	// The database also rejects completion without a matching confirmation.
	if _, err = pool.Exec(ctx, `UPDATE deliveries SET status='DELIVERED' WHERE id=$1`, d2.ID); err == nil {
		t.Fatal("DB allowed delivered without payment")
	}
}
func TestConcurrentAssignmentAndInbox(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	e := createCommand(42, 1, commands.CreateDelivery)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.ProcessCommand(ctx, e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := r.List(ctx, 0, 0, 100)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	d1 := list[0]
	d2 := accepted(t, r, 43)
	c := makeCourier(t, s)
	var successes, conflicts atomic.Int64
	for _, d := range []domain.Delivery{d1, d2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Assign(ctx, 20, d.ID, c.ID)
			if err == nil {
				successes.Add(1)
			} else if errors.Is(err, domain.ErrConflict) {
				conflicts.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatal(successes.Load(), conflicts.Load())
	}
	var assignedID int64
	if err = pool.QueryRow(ctx, `SELECT id FROM deliveries WHERE courier_id=$1`, c.ID).Scan(&assignedID); err != nil {
		t.Fatal(err)
	}
	other := makeCourier(t, s)
	if _, err = s.Assign(ctx, 20, assignedID, other.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("assigned twice", err)
	}
	bad := e
	bad.Payload.AmountToCollectMinor++
	if err = r.ProcessCommand(ctx, bad); !errors.Is(err, application.ErrEventConflict) {
		t.Fatal(err)
	}
	gap := orderEvent(42, 4, events.OrderReadyForPickup)
	if err = r.ProcessOrder(ctx, gap); !errors.Is(err, application.ErrEventGap) {
		t.Fatal(err)
	}
	if _, err = s.Assign(ctx, 99, d1.ID, other.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
}
func TestInboxRollbackAndMigration(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	e := createCommand(42, 1, commands.CreateDelivery)
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_all CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessCommand(ctx, e); err == nil {
		t.Fatal("partial delivery persisted")
	}
	for _, table := range []string{"deliveries", "processed_events", "order_streams", "command_inbox", "delivery_requests"} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal(table, n, err)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_all`); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessCommand(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Reset the full test-only schema; downgrade guards must reject nonempty command history.
	body, err := os.ReadFile("../../../migrations/000002_saga.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(body)); err == nil {
		t.Fatal("saga downgrade discarded command history")
	}

	if err := r.ProcessCommand(ctx, e); err != nil {
		t.Fatal(err)
	}
}
func TestDeliveryHTTPAuthorizationAndFullLifecycle(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	d := accepted(t, r, 42)
	c := makeCourier(t, s)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpadapter.NewHandler(s, pool.Ping, logger, 5*time.Second, true)
	call := func(method, path, header, identity, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if header != "" {
			req.Header.Set(header, identity)
		}
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	base := "/deliveries/" + strconv.FormatInt(d.ID, 10)
	if w := call("POST", base+"/assign", "", "", `{"courier_id":1}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("POST", base+"/assign", "X-Dev-User-ID", "99", `{"courier_id":1}`); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("POST", base+"/assign", "X-Dev-User-ID", "20", fmt.Sprintf(`{"courier_id":%d}`, c.ID)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("POST", base+"/complete", "", "", `{"payment_confirmed":true}`); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call("POST", base+"/pickup", "X-Dev-Courier-ID", strconv.FormatInt(c.ID+1, 10), ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	ready(t, r, 42)
	if w := call("POST", base+"/pickup", "X-Dev-Courier-ID", strconv.FormatInt(c.ID, 10), ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, body := range []string{`{}`, `{"payment_confirmed":false}`, `{"payment_confirmed":true,"amount_minor":1}`} {
		if w := call("POST", base+"/complete", "X-Dev-Courier-ID", strconv.FormatInt(c.ID, 10), body); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	for range 2 {
		w := call("POST", base+"/complete", "X-Dev-Courier-ID", strconv.FormatInt(c.ID, 10), `{"payment_confirmed":true,"pos_reference":"POS-42"}`)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var result struct {
			Confirmation struct {
				AmountMinor int64 `json:"amount_minor"`
			} `json:"confirmation"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Confirmation.AmountMinor != 25000 {
			t.Fatal(err)
		}
	}
	disabled := httpadapter.NewHandler(s, pool.Ping, logger, time.Second, false)
	req := httptest.NewRequest(http.MethodPost, base+"/complete", strings.NewReader(`{"payment_confirmed":true}`))
	req.Header.Set("X-Dev-Courier-ID", "1")
	w := httptest.NewRecorder()
	disabled.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func createCommand(order, version int64, kind string) commands.Command {
	return commands.Command{MessageID: events.NewID(), CommandType: kind, CorrelationID: "command-test", AggregateID: order, AggregateVersion: version, SchemaVersion: 1, OccurredAt: time.Now().UTC(), Payload: commands.Payload{OrderID: order, OrderVersion: 2, AmountToCollectMinor: 25000, Currency: "TJS"}}
}
