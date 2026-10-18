package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func TestPricingCRUDAndConcurrentQuoteSnapshot(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	ctx := context.Background()
	repo := NewRestaurants(pool)
	menu := NewMenu(pool)
	settings, err := repo.GetPricing(ctx, 1)
	if err != nil || settings.Settings.DeliveryEnabled || !settings.Settings.PickupEnabled || len(settings.Tariffs) != 0 {
		t.Fatal(settings, err)
	}
	lat, lon := 38.56, 68.77
	if _, err = repo.Update(ctx, 1, domain.RestaurantChanges{Latitude: &lat, Longitude: &lon}); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateSettings(ctx, 1, domain.DeliverySettings{DeliveryEnabled: true, PickupEnabled: true}); err != nil {
		t.Fatal(err)
	}
	tariff, err := repo.SaveTariff(ctx, domain.DeliveryTariff{RestaurantID: 1, MaxDistanceMeters: 2000, DeliveryFeeMinor: 1000})
	if err != nil || tariff.ID == 0 || tariff.CreatedAt.IsZero() {
		t.Fatal(tariff, err)
	}
	if _, err = repo.SaveTariff(ctx, domain.DeliveryTariff{RestaurantID: 1, MaxDistanceMeters: 2000, DeliveryFeeMinor: 2000}); !errors.Is(err, domain.ErrTariffConflict) {
		t.Fatal(err)
	}
	category, err := menu.CreateCategory(ctx, domain.MenuCategory{RestaurantID: 1, Name: "Pricing"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := menu.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: category.ID, Name: "Dish", PriceMinor: 6500, Currency: "TJS", IsAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	quotes := application.NewQuotes(menu)
	quote := func() (application.QuoteResult, error) {
		return quotes.Quote(ctx, 1, []application.QuoteItem{{MenuItemID: item.ID, Quantity: 2}}, application.QuoteOptions{FulfillmentType: "DELIVERY", DeliveryLocation: &geo.Point{Latitude: lat, Longitude: lon}})
	}
	original, err := quote()
	if err != nil || original.TotalMinor != 14000 {
		t.Fatal(original, err)
	}
	version := original.PricingSnapshot.SettingsVersion
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 40; i++ {
			v := tariff
			v.DeliveryFeeMinor = 1000 + int64(i%2)*1000
			if _, err := repo.SaveTariff(ctx, v); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 80 {
		q, err := quote()
		if err != nil {
			t.Fatal(err)
		}
		expected := int64(1000) + (q.PricingSnapshot.SettingsVersion-version)%2*1000
		if q.DeliveryFeeMinor != expected || q.TotalMinor != 13000+expected || q.PricingSnapshot.DeliveryFeeMinor != expected {
			t.Fatal("mixed pricing snapshots", q, expected)
		}
	}
	wg.Wait()
	if original.TotalMinor != 14000 || original.PricingSnapshot.DeliveryFeeMinor != 1000 {
		t.Fatal("earlier quote mutated")
	}
	if err := repo.DeleteTariff(ctx, 2, tariff.ID); !errors.Is(err, domain.ErrTariffNotFound) {
		t.Fatal(err)
	}
	if err := repo.DeleteTariff(ctx, 1, tariff.ID); err != nil {
		t.Fatal(err)
	}
	q, err := quote()
	if err != nil || q.DeliveryAvailable || q.UnavailableReason != "tariffs_missing" {
		t.Fatal(q, err)
	}
}
