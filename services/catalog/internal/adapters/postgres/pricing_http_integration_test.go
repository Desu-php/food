package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/catalog/internal/adapters/http"
	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func TestPricingHTTPAuthorizationAndCRUD(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	repo := NewRestaurants(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := httpadapter.NewHandler(application.NewRestaurants(repo), nil, pool.Ping, logger, time.Second, application.NewPricing(repo))
	own := &security.Principal{Subject: "staff", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}
	foreign := &security.Principal{Subject: "other", UserID: 11, Roles: []string{"restaurant_staff"}, Restaurants: []int64{2}}
	call := func(method, path, body string, p *security.Principal, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if p != nil {
			r = r.WithContext(security.WithPrincipal(r.Context(), *p))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	base := "/restaurants/1/delivery-settings"
	for _, route := range []struct{ method, path string }{{"GET", base}, {"PUT", base}, {"POST", "/restaurants/1/delivery-tariffs"}, {"PUT", "/restaurants/1/delivery-tariffs/1"}, {"DELETE", "/restaurants/1/delivery-tariffs/1"}} {
		call(route.method, route.path, `{}`, nil, 401)
		call(route.method, route.path, `{}`, foreign, 403)
	}
	call("PUT", base, `{"delivery_enabled":true,"pickup_enabled":true,"minimum_delivery_subtotal_minor":13000}`, own, 200)
	call("PUT", base, `{"delivery_enabled":true}`, own, 400)
	call("PUT", base, `{"delivery_enabled":true,"pickup_enabled":true,"minimum_delivery_subtotal_minor":-1}`, own, 400)
	call("POST", "/restaurants/1/delivery-tariffs", `{"max_distance_meters":2000,"delivery_fee_minor":-1}`, own, 400)
	w := call("POST", "/restaurants/1/delivery-tariffs", `{"max_distance_meters":2000,"delivery_fee_minor":0}`, own, 201)
	var tariff domain.DeliveryTariff
	if err := json.Unmarshal(w.Body.Bytes(), &tariff); err != nil {
		t.Fatal(err)
	}
	call("POST", "/restaurants/1/delivery-tariffs", `{"max_distance_meters":2000,"delivery_fee_minor":1000}`, own, 409)
	path := fmt.Sprintf("/restaurants/1/delivery-tariffs/%d", tariff.ID)
	call("PUT", path, `{"max_distance_meters":5000,"delivery_fee_minor":1500}`, own, 200)
	call("PUT", "/restaurants/1/delivery-tariffs/999", `{"max_distance_meters":5000,"delivery_fee_minor":1500}`, own, 404)
	w = call("GET", base, "", own, 200)
	var pricing application.RestaurantPricing
	if err := json.Unmarshal(w.Body.Bytes(), &pricing); err != nil || len(pricing.Tariffs) != 1 || pricing.Tariffs[0].DeliveryFeeMinor != 1500 {
		t.Fatal(pricing, err)
	}
	call("DELETE", path, "", own, 204)
	call("DELETE", path, "", own, 404)
	call("PATCH", base, `{}`, own, 405)
	// Private v2 quote requires a fulfillment type and paired delivery coordinates.
	internal := httpadapter.NewInternalHandler(application.NewQuotes(NewMenu(pool)), logger, time.Second)
	r := httptest.NewRequest("POST", "/internal/v2/menu/quote", strings.NewReader(`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":1}]}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	internal.ServeHTTP(rec, r)
	if rec.Code != 400 {
		t.Fatal(rec.Code, rec.Body)
	}
	if _, err := repo.GetPricing(context.Background(), 999); err == nil {
		t.Fatal("missing restaurant accepted")
	}
}
