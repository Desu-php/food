package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

// This small repository is only for transport tests. PostgreSQL concurrency and
// atomicity are verified separately against the real adapter.
type testRepository struct {
	mu       sync.Mutex
	order    domain.Order
	creation domain.Order
	hash     string
}

func (r *testRepository) FindCreation(_ context.Context, _ int64, _ string, hash string) (domain.Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hash != "" && r.hash != hash {
		return domain.Order{}, false, application.ErrIdempotencyConflict
	}
	return r.creation, r.hash != "", nil
}
func (r *testRepository) Create(_ context.Context, _ string, hash string, o domain.Order) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := o.Snapshot()
	s.ID = 1
	for i := range s.Items {
		s.Items[i].ID = int64(i + 1)
		s.Items[i].OrderID = 1
	}
	r.order, _ = domain.Restore(s)
	r.creation = r.order
	r.hash = hash
	return r.order, nil
}
func (r *testRepository) Get(_ context.Context, customer, id int64) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.order.Snapshot()
	if s.ID != id || s.CustomerID != customer {
		return domain.Order{}, domain.ErrNotFound
	}
	return r.order, nil
}
func (r *testRepository) Cancel(_ context.Context, customer, id int64, change application.FulfillmentMutation) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.order.Snapshot()
	if s.ID != id || s.CustomerID != customer {
		return domain.Order{}, domain.ErrNotFound
	}
	saga := domain.NewSaga(s.ID, time.Now())
	_, err := change(&r.order, &saga)
	return r.order, err
}

type testCatalog struct{ err error }

func (c testCatalog) Quote(_ context.Context, id int64, items []application.RequestedItem, options ...application.QuoteOptions) (application.CatalogQuote, error) {
	if c.err != nil {
		return application.CatalogQuote{}, c.err
	}
	result := make([]domain.PricedItem, len(items))
	for i, p := range items {
		result[i] = domain.PricedItem{MenuItemID: p.MenuItemID, RestaurantID: id, Name: "Плов", Quantity: p.Quantity, UnitPriceMinor: 6500, Currency: "TJS"}
	}
	var subtotal int64
	for _, i := range result {
		subtotal += i.UnitPriceMinor * i.Quantity
	}
	snap := &domain.PricingSnapshot{SettingsVersion: 1}
	var location *geo.Point
	if len(options) > 0 && options[0].FulfillmentType == domain.Delivery {
		d := int64(0)
		snap.TariffID = 1
		snap.MaxDistanceMeters = 2000
		snap.DistanceMeters = &d
		location = &geo.Point{Latitude: 38.56, Longitude: 68.77}
	}
	return application.CatalogQuote{Items: result, PickupAddress: "Restaurant location", DeliveryAvailable: true, ItemsSubtotalMinor: subtotal, TotalMinor: subtotal, PricingSnapshot: snap, RestaurantLocation: location}, nil
}
func newHandler(repo *testRepository, catalogErr error, dev bool, readyErr error) http.Handler {
	return NewHandler(application.NewOrders(repo, testCatalog{catalogErr}), func(context.Context) error { return readyErr }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, dev)
}

const createBody = `{"restaurant_id":1,"fulfillment_type":"DELIVERY","delivery_address":"Test delivery address","currency":"TJS","expected_total_minor":13000,"delivery_latitude":38.56,"delivery_longitude":68.77,"items":[{"menu_item_id":100,"quantity":2}]}`

func request(h http.Handler, method, path, body, customer, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if customer != "" {
		r.Header.Set("X-Dev-Customer-ID", customer)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestOrderHTTPWorkflow(t *testing.T) {
	repo := &testRepository{}
	h := newHandler(repo, nil, true, nil)
	created := request(h, "POST", "/orders", createBody, "7", "key")
	if created.Code != 201 || created.Header().Get("Location") != "/orders/1" {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var dto orderDTO
	if err := json.Unmarshal(created.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.SubtotalMinor != 13000 || dto.CustomerID != 7 || dto.Status != domain.WaitingRestaurant || dto.Items[0].Name != "Плов" {
		t.Fatal(dto)
	}
	replay := request(h, "POST", "/orders", createBody, "7", "key")
	if replay.Code != 201 || replay.Body.String() != created.Body.String() {
		t.Fatal("replay changed")
	}
	if w := request(h, "GET", "/orders/1", "", "8", ""); w.Code != 404 {
		t.Fatal("other customer can read order")
	}
	if w := request(h, "POST", "/orders/1/cancel", "", "8", ""); w.Code != 404 {
		t.Fatal("other customer can cancel order")
	}
	if w := request(h, "GET", "/orders/1", "", "7", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(h, "POST", "/orders/1/cancel", "", "7", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "CANCELLED") {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(h, "POST", "/orders/1/cancel", "", "7", ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(h, "POST", "/orders", createBody, "7", "key"); w.Body.String() != created.Body.String() {
		t.Fatal("replay after cancel did not return original result")
	}
}
func TestOrderHTTPErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body, customer, key string
		dev                       bool
		failure                   error
		status                    int
	}{
		{"empty", `{"restaurant_id":1,"fulfillment_type":"DELIVERY","delivery_address":"Test delivery address","currency":"TJS","expected_subtotal_minor":1,"items":[]}`, "7", "key", true, nil, 400},
		{"client price", strings.Replace(createBody, `"quantity":2`, `"quantity":2,"unit_price_minor":1`, 1), "7", "key", true, nil, 400},
		{"client identity", strings.Replace(createBody, `"restaurant_id":1`, `"restaurant_id":1,"customer_id":99`, 1), "7", "key", true, nil, 400},
		{"missing expected", strings.Replace(createBody, `"expected_total_minor":13000,"delivery_latitude":38.56,"delivery_longitude":68.77,`, "", 1), "7", "key", true, nil, 400},
		{"price changed", strings.Replace(createBody, "13000", "12000", 1), "7", "key", true, nil, 409},
		{"missing identity", createBody, "", "key", true, nil, 401},
		{"production identity", createBody, "7", "key", false, nil, 401},
		{"missing key", createBody, "7", "", true, nil, 400},
		{"missing restaurant", createBody, "7", "key", true, application.ErrRestaurantNotFound, 404},
		{"missing item", createBody, "7", "key", true, application.ErrItemNotFound, 404},
		{"unavailable item", createBody, "7", "key", true, application.ErrItemUnavailable, 409},
		{"foreign item", createBody, "7", "key", true, application.ErrItemRestaurantMismatch, 409},
		{"catalog outage", createBody, "7", "key", true, application.ErrCatalogUnavailable, 503},
		{"internal error", createBody, "7", "key", true, errors.New("secret connection string"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := request(newHandler(&testRepository{}, tt.failure, tt.dev, nil), "POST", "/orders", tt.body, tt.customer, tt.key)
			if w.Code != tt.status || !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
	h := newHandler(&testRepository{}, nil, true, errors.New("database unavailable"))
	if w := request(h, "GET", "/health", "", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/ready", "", "", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := request(h, "PATCH", "/orders/1", "", "7", ""); w.Code != 405 || w.Header().Get("Allow") == "" {
		t.Fatal(w.Code)
	}
}

func (r *testRepository) IssuePickupCode(context.Context, int64, int64, []byte, time.Time) error {
	return nil
}

func TestFulfillmentHTTPValidation(t *testing.T) {
	for _, body := range []string{
		strings.Replace(createBody, `"fulfillment_type":"DELIVERY",`, "", 1),
		strings.Replace(createBody, `"delivery_address":"Test delivery address",`, "", 1),
		strings.Replace(createBody, `"fulfillment_type":"DELIVERY"`, `"fulfillment_type":"PICKUP"`, 1),
		strings.Replace(createBody, `"fulfillment_type":"DELIVERY"`, `"fulfillment_type":"OTHER"`, 1),
		strings.Replace(createBody, `"restaurant_id":1`, `"restaurant_id":1,"payment_method":"POS_AT_PICKUP"`, 1),
		strings.Replace(createBody, `"restaurant_id":1`, `"restaurant_id":1,"pickup_address":"Client location"`, 1),
		strings.Replace(createBody, `"restaurant_id":1`, `"restaurant_id":1,"delivery_fee_minor":0`, 1),
	} {
		w := request(newHandler(&testRepository{}, nil, true, nil), "POST", "/orders", body, "7", "key")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	body := strings.Replace(createBody, `"fulfillment_type":"DELIVERY","delivery_address":"Test delivery address",`, `"fulfillment_type":"PICKUP",`, 1)
	body = strings.Replace(body, `"delivery_latitude":38.56,"delivery_longitude":68.77,`, "", 1)
	w := request(newHandler(&testRepository{}, nil, true, nil), "POST", "/orders", body, "7", "key")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var dto orderDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.FulfillmentType != domain.Pickup || dto.PaymentMethod != domain.POSAtPickup || dto.DeliveryFeeMinor != 0 || dto.DeliveryAddress != "" || dto.PickupAddress != "Restaurant location" {
		t.Fatal(dto)
	}
}
