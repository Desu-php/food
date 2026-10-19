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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	catalogadapter "github.com/Desu-php/food/services/orders/internal/adapters/catalog"
	httpadapter "github.com/Desu-php/food/services/orders/internal/adapters/http"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func integrationPool(t *testing.T, migrations ...string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("orders_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if len(migrations) == 0 {
		migrations = []string{"000001_orders", "000002_outbox", "000003_fulfillment", "000004_saga", "000005_pickup", "000006_consumer_quarantine", "000007_dispatch_snapshot", "000008_pricing", "000009_push_events"}
	}
	for _, migration := range migrations {
		sql, err := os.ReadFile("../../../migrations/" + migration + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return pool
}

func newOrder(t *testing.T) domain.Order {
	t.Helper()
	o, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 10, RestaurantID: 1, Name: "Плов", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}, {MenuItemID: 11, RestaurantID: 1, Name: "Чай", Quantity: 1, UnitPriceMinor: 100, Currency: "TJS"}}, time.Now(), domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Test delivery address"})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func TestOrderRepositoryPersistence(t *testing.T) {
	pool := integrationPool(t)
	repo := NewOrders(pool)
	ctx := context.Background()
	hash := strings.Repeat("a", 64)
	created, err := repo.Create(ctx, "key", hash, newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	state := created.Snapshot()
	if state.ID <= 0 || len(state.Items) != 2 || state.SubtotalMinor != 13100 || state.Items[0].ID <= 0 || state.Items[0].OrderID != state.ID {
		t.Fatal(state)
	}
	restarted := NewOrders(pool)
	got, found, err := restarted.FindCreation(ctx, 7, "key", hash)
	if err != nil || !found || got.Snapshot().ID != state.ID {
		t.Fatalf("restart: %+v %v %v", got, found, err)
	}
	if _, _, err := repo.FindCreation(ctx, 7, "key", strings.Repeat("b", 64)); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, 8, state.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("ownership", err)
	}
	if _, err := repo.Get(ctx, 7, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	svc := application.NewOrders(repo, nil)
	cancelled, err := svc.CancelOrder(ctx, 7, state.ID)
	if err != nil || cancelled.Snapshot().Status != domain.Cancelled {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if _, err := svc.CancelOrder(ctx, 7, state.ID); !errors.Is(err, domain.ErrCannotCancel) {
		t.Fatal(err)
	}
	replay, found, err := restarted.FindCreation(ctx, 7, "key", hash)
	if err != nil || !found || replay.Snapshot().Status != domain.WaitingRestaurant || replay.Snapshot().Items[0].Name != "Плов" {
		t.Fatalf("original response changed: %+v %v", replay, err)
	}
	current, err := repo.Get(ctx, 7, state.ID)
	if err != nil || current.Snapshot().Status != domain.Cancelled {
		t.Fatal("cancellation not persisted", err)
	}
	// Identical keys belong to different customers independently.
	items := []domain.PricedItem{{MenuItemID: 10, RestaurantID: 1, Name: "Чай", Quantity: 1, UnitPriceMinor: 100, Currency: "TJS"}}
	other, _ := domain.NewOrder(8, 1, "TJS", items, time.Now(), domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Test delivery address"})
	if _, err := repo.Create(ctx, "key", hash, other); err != nil {
		t.Fatal(err)
	}
}

func TestOrderAtomicityAndConstraints(t *testing.T) {
	pool := integrationPool(t)
	repo := NewOrders(pool)
	ctx := context.Background()
	// Failure at the very last write must roll back both aggregate tables.
	if _, err := repo.Create(ctx, "key", "invalid-hash", newOrder(t)); err == nil {
		t.Fatal("accepted invalid fingerprint")
	}
	for _, table := range []string{"orders", "order_items", "idempotency_keys", "outbox_events"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial write to %s: %d %v", table, count, err)
		}
	}
	created, err := repo.Create(ctx, "key", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Snapshot().ID
	for _, query := range []string{
		`UPDATE orders SET currency='USD' WHERE id=$1`,
		`UPDATE orders SET status='PAID' WHERE id=$1`,
		`UPDATE orders SET subtotal_minor=-1 WHERE id=$1`,
		`UPDATE order_items SET quantity=0 WHERE order_id=$1`,
		`UPDATE order_items SET quantity=101 WHERE order_id=$1`,
		`UPDATE order_items SET total_price_minor=1 WHERE order_id=$1`,
		`UPDATE order_items SET order_id=999999 WHERE order_id=$1`,
		`UPDATE idempotency_keys SET order_id=999999 WHERE order_id=$1`,
	} {
		if _, err := pool.Exec(ctx, query, id); err == nil {
			t.Fatalf("constraint missing: %s", query)
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.Get(cancelCtx, 7, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentOrderCreationHTTP(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const count = 8
	allQuoted := make(chan struct{})
	var price atomic.Int64
	price.Store(6500)
	var calls atomic.Int64
	catalogServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= count {
			if n == count {
				close(allQuoted)
			}
			select {
			case <-allQuoted:
			case <-r.Context().Done():
				return
			}
		}
		// A catalog call must not retain a SQL transaction or checked-out connection.
		if _, err := pool.Exec(r.Context(), "SELECT 1"); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"restaurant_id": 1, "currency": "TJS", "restaurant_location": map[string]any{"latitude": 38.56, "longitude": 68.77}, "delivery_available": true, "items_subtotal_minor": price.Load() * 2, "total_minor": price.Load() * 2, "pricing_snapshot": map[string]any{"settings_version": 1, "tariff_id": 1, "max_distance_meters": 2000, "distance_meters": 0, "delivery_fee_minor": 0}, "items": []map[string]any{{"menu_item_id": 10, "restaurant_id": 1, "name": "Original name", "quantity": 2, "unit_price_minor": price.Load(), "currency": "TJS"}}})
	}))
	defer catalogServer.Close()
	client, err := catalogadapter.NewClient(catalogServer.URL, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	svc := application.NewOrders(NewOrders(pool), client)
	server := httptest.NewServer(httpadapter.NewHandler(svc, pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), 15*time.Second, true))
	defer server.Close()
	body := `{"restaurant_id":1,"fulfillment_type":"DELIVERY","delivery_address":"Test delivery address","currency":"TJS","expected_total_minor":13000,"delivery_latitude":38.56,"delivery_longitude":68.77,"items":[{"menu_item_id":10,"quantity":2}]}`
	post := func(key, payload string) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/orders", strings.NewReader(payload))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dev-Customer-ID", "7")
		req.Header.Set("Idempotency-Key", key)
		response, err := server.Client().Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		return response.StatusCode, data, err
	}
	var wg sync.WaitGroup
	results := make(chan []byte, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, data, err := post("concurrent", body)
			if err != nil || status != 201 {
				t.Errorf("create: %d %s %v", status, data, err)
				return
			}
			results <- data
		}()
	}
	wg.Wait()
	close(results)
	var original []byte
	for data := range results {
		if original == nil {
			original = data
		}
		if string(original) != string(data) {
			t.Error("concurrent responses differ")
		}
	}
	if original == nil {
		t.Fatal("no successful responses")
	}
	for table, want := range map[string]int{"orders": 1, "order_items": 1, "idempotency_keys": 1, "outbox_events": 1} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: %d %v", table, n, err)
		}
	}
	price.Store(7000)
	status, data, err := post("concurrent", body)
	if err != nil || status != 201 || string(data) != string(original) {
		t.Fatalf("price changed replay: %d %s %v", status, data, err)
	}
	if status, _, err := post("new-price", body); err != nil || status != 409 {
		t.Fatalf("price mismatch: %d %v", status, err)
	}
	if status, _, err := post("concurrent", strings.Replace(body, "13000", "14000", 1)); err != nil || status != 409 {
		t.Fatalf("key conflict: %d %v", status, err)
	}
	if status, _, err := post("new-price", strings.Replace(body, "13000", "14000", 1)); err != nil || status != 201 {
		t.Fatalf("new confirmation: %d %v", status, err)
	}
	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(original, &result); err != nil {
		t.Fatal(err)
	}
	// Concurrent cancellations apply exactly one transition.
	successes, conflicts := atomic.Int64{}, atomic.Int64{}
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CancelOrder(ctx, 7, result.ID)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, domain.ErrCannotCancel):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != count-1 {
		t.Fatalf("cancellation results: %d %d", successes.Load(), conflicts.Load())
	}
	catalogServer.Close()
	restarted := application.NewOrders(NewOrders(pool), client)
	replay, err := restarted.CreateOrder(ctx, application.CreateCommand{ExpectedTotalMinor: ptrInt64(13000), DeliveryLatitude: ptrFloat64(38.56), DeliveryLongitude: ptrFloat64(68.77), FulfillmentType: domain.Delivery, DeliveryAddress: "Test delivery address", CustomerID: 7, RestaurantID: 1, Currency: "TJS", Items: []application.RequestedItem{{MenuItemID: 10, Quantity: 2}}}, "concurrent")
	if err != nil || replay.Snapshot().Status != domain.WaitingRestaurant || replay.Snapshot().Items[0].UnitPriceMinor != 6500 || replay.Snapshot().Items[0].Name != "Original name" {
		t.Fatalf("replay after restart/outage/cancel: %+v %v", replay, err)
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&eventCount); err != nil || eventCount != 3 {
		t.Fatalf("idempotent replay or cancellation created extra events: %d %v", eventCount, err)
	}
	if _, err := restarted.GetOrder(ctx, 7, result.ID); err != nil {
		t.Fatal("get depends on Catalog", err)
	}
}

func TestConcurrentDifferentPayloads(t *testing.T) {
	pool := integrationPool(t)
	repo := NewOrders(pool)
	ctx := context.Background()
	var wg sync.WaitGroup
	var success, conflict atomic.Int64
	for _, hash := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Create(ctx, "same-key", hash, newOrder(t))
			if err == nil {
				success.Add(1)
			} else if errors.Is(err, application.ErrIdempotencyConflict) {
				conflict.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || conflict.Load() != 1 {
		t.Fatalf("results: %d %d", success.Load(), conflict.Load())
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&n); err != nil || n != 1 {
		t.Fatalf("orders: %d %v", n, err)
	}
}
func TestOrdersMigrationRoundTrip(t *testing.T) {
	pool := integrationPool(t, "000001_orders")
	ctx := context.Background()
	down, err := os.ReadFile("../../../migrations/000001_orders.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('orders') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("down: %v %v", exists, err)
	}
	up, err := os.ReadFile("../../../migrations/000001_orders.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	outbox, err := os.ReadFile("../../../migrations/000002_outbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(outbox)); err != nil {
		t.Fatal(err)
	}
	fulfillment, err := os.ReadFile("../../../migrations/000003_fulfillment.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(fulfillment)); err != nil {
		t.Fatal(err)
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
	applyDispatchMigration(t, pool)
	if _, err := NewOrders(pool).Create(ctx, "after-up", strings.Repeat("a", 64), newOrder(t)); err != nil {
		t.Fatal(err)
	}
}

func ptrInt64(v int64) *int64       { return &v }
func ptrFloat64(v float64) *float64 { return &v }
