package application

import (
	"context"
	"errors"

	"example.com/food/catalog/internal/domain"
)

var (
	ErrInvalidQuote           = errors.New("quote requires a positive restaurant ID and 1 to 100 unique items with quantity between 1 and 100")
	ErrRestaurantUnavailable  = errors.New("restaurant is not accepting orders")
	ErrItemUnavailable        = errors.New("menu item unavailable")
	ErrItemRestaurantMismatch = errors.New("menu item belongs to another restaurant")
)

type QuoteItem struct {
	MenuItemID int64 `json:"menu_item_id"`
	Quantity   int64 `json:"quantity"`
}

type QuoteRepository interface {
	LoadQuote(context.Context, int64, []int64) (domain.Restaurant, []domain.MenuItem, error)
}

type QuoteResult struct {
	Items         []domain.MenuItem
	PickupAddress string
}

type Quotes struct{ repo QuoteRepository }

func NewQuotes(repo QuoteRepository) *Quotes { return &Quotes{repo: repo} }
func (s *Quotes) Quote(ctx context.Context, restaurantID int64, items []QuoteItem) (QuoteResult, error) {
	if restaurantID <= 0 || len(items) == 0 || len(items) > 100 {
		return QuoteResult{}, ErrInvalidQuote
	}
	ids := make([]int64, len(items))
	seen := make(map[int64]bool, len(items))
	for i, p := range items {
		if p.MenuItemID <= 0 || p.Quantity < 1 || p.Quantity > 100 || seen[p.MenuItemID] {
			return QuoteResult{}, ErrInvalidQuote
		}
		seen[p.MenuItemID] = true
		ids[i] = p.MenuItemID
	}
	restaurant, menu, err := s.repo.LoadQuote(ctx, restaurantID, ids)
	if err != nil {
		return QuoteResult{}, err
	}
	if !restaurant.IsOpen {
		return QuoteResult{}, ErrRestaurantUnavailable
	}
	if len(menu) != len(items) {
		return QuoteResult{}, domain.ErrItemNotFound
	}
	for _, p := range menu {
		if p.RestaurantID != restaurantID {
			return QuoteResult{}, ErrItemRestaurantMismatch
		}
		if !p.IsAvailable {
			return QuoteResult{}, ErrItemUnavailable
		}
	}
	return QuoteResult{Items: menu, PickupAddress: restaurant.Address}, nil
}
