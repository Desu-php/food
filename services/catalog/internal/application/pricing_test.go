package application

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func pricingRepo() quoteRepo {
	lat, lon := 38.56, 68.77
	return quoteRepo{restaurant: domain.Restaurant{ID: 1, IsOpen: true, Address: "Restaurant", Latitude: &lat, Longitude: &lon, Settings: domain.DeliverySettings{DeliveryEnabled: true, PickupEnabled: true, Version: 4}, Tariffs: []domain.DeliveryTariff{{ID: 1, MaxDistanceMeters: 2000, DeliveryFeeMinor: 1000}}}, items: []domain.MenuItem{{ID: 10, RestaurantID: 1, Name: "Dish", PriceMinor: 6500, Currency: "TJS", IsAvailable: true}}}
}
func TestFulfillmentPricing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*quoteRepo, *QuoteOptions)
		reason string
		fee    int64
	}{
		{"delivery", func(*quoteRepo, *QuoteOptions) {}, "", 1000},
		{"disabled", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Settings.DeliveryEnabled = false }, "delivery_disabled", 0},
		{"minimum", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Settings.MinimumDeliverySubtotalMinor = 13001 }, "minimum_delivery_subtotal", 1000},
		{"minimum inclusive", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Settings.MinimumDeliverySubtotalMinor = 13000 }, "", 1000},
		{"outside", func(_ *quoteRepo, o *QuoteOptions) { o.DeliveryLocation = &geo.Point{} }, "outside_delivery_zone", 0},
		{"missing location", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Latitude = nil; r.restaurant.Longitude = nil }, "restaurant_coordinates_missing", 0},
		{"missing tariffs", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Tariffs = nil }, "tariffs_missing", 0},
		{"free", func(r *quoteRepo, _ *QuoteOptions) { r.restaurant.Tariffs[0].DeliveryFeeMinor = 0 }, "", 0},
		{"pickup", func(r *quoteRepo, o *QuoteOptions) {
			o.FulfillmentType = "PICKUP"
			o.DeliveryLocation = nil
			r.restaurant.Latitude = nil
			r.restaurant.Settings.MinimumDeliverySubtotalMinor = math.MaxInt64
		}, "", 0},
		{"pickup disabled", func(r *quoteRepo, o *QuoteOptions) {
			o.FulfillmentType = "PICKUP"
			o.DeliveryLocation = nil
			r.restaurant.Settings.PickupEnabled = false
		}, "pickup_disabled", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pricingRepo()
			o := QuoteOptions{FulfillmentType: "DELIVERY", DeliveryLocation: &geo.Point{Latitude: 38.56, Longitude: 68.77}}
			tc.change(&r, &o)
			got, err := NewQuotes(r).Quote(context.Background(), 1, []QuoteItem{{MenuItemID: 10, Quantity: 2}}, o)
			if err != nil || got.UnavailableReason != tc.reason || got.DeliveryAvailable != (tc.reason == "") || got.ItemsSubtotalMinor != 13000 || got.DeliveryFeeMinor != tc.fee || got.TotalMinor != 13000+tc.fee || got.PricingSnapshot.SettingsVersion != 4 {
				t.Fatal(got, err)
			}
		})
	}
}
func TestQuotePricingOverflowAndCoordinates(t *testing.T) {
	for _, price := range []int64{math.MaxInt64, math.MaxInt64 / 2} {
		r := pricingRepo()
		r.items[0].PriceMinor = price
		_, err := NewQuotes(r).Quote(context.Background(), 1, []QuoteItem{{MenuItemID: 10, Quantity: 2}}, QuoteOptions{FulfillmentType: "DELIVERY", DeliveryLocation: &geo.Point{Latitude: 38.56, Longitude: 68.77}})
		if !errors.Is(err, domain.ErrAmountOverflow) {
			t.Fatal(err)
		}
	}
	for _, point := range []*geo.Point{nil, {Latitude: math.NaN()}, {Longitude: math.Inf(1)}, {Latitude: 91}} {
		_, err := NewQuotes(pricingRepo()).Quote(context.Background(), 1, []QuoteItem{{MenuItemID: 10, Quantity: 1}}, QuoteOptions{FulfillmentType: "DELIVERY", DeliveryLocation: point})
		if !errors.Is(err, domain.ErrCoordinates) {
			t.Fatal(err)
		}
	}
}
