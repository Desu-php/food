package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func TestCatalogQuote(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	ctx := context.Background()
	repo := NewMenu(pool)
	quotes := application.NewQuotes(repo)
	category, err := repo.CreateCategory(ctx, domain.MenuCategory{RestaurantID: 1, Name: "Menu"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: category.ID, Name: "Плов", PriceMinor: 6500, Currency: "TJS", IsAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: category.ID, Name: "Чай", PriceMinor: 100, Currency: "TJS", IsAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	requested := []application.QuoteItem{{MenuItemID: a.ID, Quantity: 2}, {MenuItemID: b.ID, Quantity: 1}}
	got, err := quotes.Quote(ctx, 1, requested)
	if err != nil || len(got.Items) != 2 || got.Items[0].PriceMinor != 6500 || got.Items[1].Name != "Чай" {
		t.Fatalf("batch: %+v %v", got, err)
	}
	if _, err := quotes.Quote(ctx, 999, requested); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := quotes.Quote(ctx, 2, requested); !errors.Is(err, application.ErrRestaurantUnavailable) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE restaurants SET is_open=true WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := quotes.Quote(ctx, 2, requested); !errors.Is(err, application.ErrItemRestaurantMismatch) {
		t.Fatal(err)
	}
	if _, err := quotes.Quote(ctx, 1, []application.QuoteItem{{MenuItemID: 999, Quantity: 1}}); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
	price := int64(7000)
	name := "New name"
	if _, err := repo.UpdateItem(ctx, 1, a.ID, domain.MenuItemChanges{PriceMinor: &price, Name: &name}); err != nil {
		t.Fatal(err)
	}
	got, err = quotes.Quote(ctx, 1, requested)
	if err != nil || got.Items[0].PriceMinor != 7000 || got.Items[0].Name != name {
		t.Fatalf("stale quote: %+v %v", got, err)
	}
	unavailable := false
	if _, err := repo.UpdateItem(ctx, 1, a.ID, domain.MenuItemChanges{IsAvailable: &unavailable}); err != nil {
		t.Fatal(err)
	}
	if _, err := quotes.Quote(ctx, 1, requested); !errors.Is(err, application.ErrItemUnavailable) {
		t.Fatal(err)
	}
	if err := repo.DeleteItem(ctx, 1, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := quotes.Quote(ctx, 1, requested); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
}
