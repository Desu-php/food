package application

import (
	"context"
	"errors"
	"testing"

	"example.com/food/catalog/internal/domain"
)

type quoteRepo struct {
	restaurant domain.Restaurant
	items      []domain.MenuItem
	err        error
}

func (r quoteRepo) LoadQuote(context.Context, int64, []int64) (domain.Restaurant, []domain.MenuItem, error) {
	return r.restaurant, r.items, r.err
}
func TestQuoteRules(t *testing.T) {
	for _, tt := range []struct {
		name string
		repo quoteRepo
		want error
	}{
		{"success", quoteRepo{domain.Restaurant{ID: 1, IsOpen: true}, []domain.MenuItem{{ID: 10, RestaurantID: 1, Name: "Плов", PriceMinor: 6500, Currency: "TJS", IsAvailable: true}}, nil}, nil},
		{"missing restaurant", quoteRepo{err: domain.ErrNotFound}, domain.ErrNotFound},
		{"missing item", quoteRepo{err: domain.ErrItemNotFound}, domain.ErrItemNotFound},
		{"closed restaurant", quoteRepo{restaurant: domain.Restaurant{ID: 1}}, ErrRestaurantUnavailable},
		{"foreign item", quoteRepo{restaurant: domain.Restaurant{ID: 1, IsOpen: true}, items: []domain.MenuItem{{ID: 10, RestaurantID: 2, IsAvailable: true}}}, ErrItemRestaurantMismatch},
		{"unavailable item", quoteRepo{restaurant: domain.Restaurant{ID: 1, IsOpen: true}, items: []domain.MenuItem{{ID: 10, RestaurantID: 1}}}, ErrItemUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewQuotes(tt.repo).Quote(context.Background(), 1, []QuoteItem{{MenuItemID: 10, Quantity: 2}})
			if !errors.Is(err, tt.want) {
				t.Fatalf("%v, want %v", err, tt.want)
			}
		})
	}
	for _, items := range [][]QuoteItem{nil, {{MenuItemID: 1, Quantity: 0}}, {{MenuItemID: 1, Quantity: 101}}, {{MenuItemID: 1, Quantity: 1}, {MenuItemID: 1, Quantity: 2}}} {
		if _, err := NewQuotes(quoteRepo{}).Quote(context.Background(), 1, items); !errors.Is(err, ErrInvalidQuote) {
			t.Fatalf("validation: %v", err)
		}
	}
}
