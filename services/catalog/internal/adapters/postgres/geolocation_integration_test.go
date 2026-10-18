package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpadapter "example.com/food/catalog/internal/adapters/http"
	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
	"example.com/food/platform/security"
)

func TestAuthorizedCoordinatesAndQuoteSnapshot(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	r := NewRestaurants(pool)
	ctx := context.Background()
	service := application.NewRestaurants(r)
	restaurant, err := service.Create(ctx, "Restaurant", true, "Existing restaurant address")
	if err != nil {
		t.Fatal(err)
	}
	h := httpadapter.NewHandler(service, application.NewMenu(r, NewMenu(pool)), pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	for _, tc := range []struct {
		restaurant int64
		body       string
		code       int
	}{
		{restaurant.ID + 1, `{"latitude":38.56,"longitude":68.77}`, 403},
		{restaurant.ID, `{"latitude":38.56}`, 400},
		{restaurant.ID, `{"latitude":38.56,"longitude":181}`, 400},
		{restaurant.ID, `{"latitude":38.56,"longitude":68.77}`, 200},
	} {
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/restaurants/%d", restaurant.ID), strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{Subject: "restaurant-employee", Roles: []string{"restaurant_staff"}, Restaurants: []int64{tc.restaurant}}))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatal(res.Code, res.Body)
		}
	}
	got, err := r.Get(ctx, restaurant.ID)
	if err != nil || got.Latitude == nil || *got.Latitude != 38.56 || got.Address != restaurant.Address {
		t.Fatal(got, err)
	}
	menu := NewMenu(pool)
	category, err := menu.CreateCategory(ctx, domain.MenuCategory{RestaurantID: restaurant.ID, Name: "Menu"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := menu.CreateItem(ctx, domain.MenuItem{RestaurantID: restaurant.ID, CategoryID: category.ID, Name: "Dish", PriceMinor: 6500, Currency: "TJS", IsAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := application.NewQuotes(menu).Quote(ctx, restaurant.ID, []application.QuoteItem{{MenuItemID: item.ID, Quantity: 1}})
	if err != nil || quote.RestaurantLocation == nil || quote.RestaurantLocation.Latitude != 38.56 || quote.PickupAddress != restaurant.Address {
		t.Fatal(quote, err)
	}
	// SQL enforces the pair as well as the domain.
	if _, err = pool.Exec(ctx, `UPDATE restaurants SET latitude=NULL WHERE id=$1`, restaurant.ID); err == nil {
		t.Fatal("invalid coordinate pair persisted")
	}
}
