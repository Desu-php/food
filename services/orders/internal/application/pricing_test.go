package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

type pricingCatalog struct {
	fee       int64
	available bool
}

func (p *pricingCatalog) Quote(_ context.Context, _ int64, _ []RequestedItem, _ ...QuoteOptions) (CatalogQuote, error) {
	distance := int64(0)
	return CatalogQuote{Items: []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Dish", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, ItemsSubtotalMinor: 13000, DeliveryFeeMinor: p.fee, TotalMinor: 13000 + p.fee, DeliveryAvailable: p.available, RestaurantLocation: &geo.Point{Latitude: 38.56, Longitude: 68.77}, PricingSnapshot: &domain.PricingSnapshot{SettingsVersion: 2, TariffID: 1, MaxDistanceMeters: 2000, DistanceMeters: &distance, DeliveryFeeMinor: p.fee}}, nil
}
func TestTariffChangeKeepsCreatedOrderAndRequiresNewTotal(t *testing.T) {
	ctx := context.Background()
	repo := &repositoryStub{}
	catalog := &pricingCatalog{fee: 1000, available: true}
	service := NewOrders(repo, catalog)
	c := command()
	total := int64(14000)
	c.ExpectedTotalMinor = &total
	c.ExpectedSubtotalMinor = 0
	created, err := service.CreateOrder(ctx, c, "pricing")
	if err != nil || created.Snapshot().TotalMinor != 14000 {
		t.Fatal(created, err)
	}
	catalog.fee = 2000
	replay, err := service.CreateOrder(ctx, c, "pricing")
	if err != nil || replay.Snapshot().TotalMinor != 14000 || replay.Snapshot().DeliveryFeeMinor != 1000 {
		t.Fatal(replay, err)
	}
	repo.found = false
	_, err = service.CreateOrder(ctx, c, "new-pricing")
	var changed *PriceChangedError
	if !errors.Is(err, ErrPriceChanged) || !errors.As(err, &changed) || changed.TotalMinor != 15000 || repo.saved != 1 {
		t.Fatal(err, repo.saved)
	}
	total = 15000
	o, err := service.CreateOrder(ctx, c, "new-pricing")
	if err != nil || o.Snapshot().TotalMinor != 15000 {
		t.Fatal(o, err)
	}
	repo.found = false
	catalog.available = false
	if _, err = service.CreateOrder(ctx, c, "disabled"); !errors.Is(err, ErrFulfillmentUnavailable) {
		t.Fatal(err)
	}
}
func TestOldPricingShapeCannotCreateNewOrder(t *testing.T) {
	c := command()
	c.ExpectedTotalMinor = nil
	if _, err := NewOrders(&repositoryStub{}, nil).CreateOrder(context.Background(), c, "new"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
}
