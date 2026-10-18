package application

import (
	"context"
	"errors"
	"math"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/catalog/internal/domain"
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

type QuoteOptions struct {
	FulfillmentType  string     `json:"fulfillment_type"`
	DeliveryLocation *geo.Point `json:"delivery_location,omitempty"`
}

type QuoteResult struct {
	ItemsSubtotalMinor int64
	DeliveryFeeMinor   int64
	TotalMinor         int64
	DeliveryAvailable  bool
	UnavailableReason  string
	DistanceMeters     *int64
	PricingSnapshot    *domain.PricingSnapshot
	RestaurantLocation *geo.Point
	Items              []domain.MenuItem
	PickupAddress      string
}

type Quotes struct{ repo QuoteRepository }

func NewQuotes(repo QuoteRepository) *Quotes { return &Quotes{repo: repo} }
func (s *Quotes) Quote(ctx context.Context, restaurantID int64, items []QuoteItem, options ...QuoteOptions) (QuoteResult, error) {
	if len(options) > 0 {
		o := options[0]
		if o.FulfillmentType != "DELIVERY" && o.FulfillmentType != "PICKUP" {
			return QuoteResult{}, ErrInvalidQuote
		}
		if o.FulfillmentType == "DELIVERY" && (o.DeliveryLocation == nil || !o.DeliveryLocation.Valid()) {
			return QuoteResult{}, domain.ErrCoordinates
		}
		if o.FulfillmentType == "PICKUP" && o.DeliveryLocation != nil {
			return QuoteResult{}, ErrInvalidQuote
		}
	}
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
	var location *geo.Point
	if restaurant.Latitude != nil && restaurant.Longitude != nil {
		location = &geo.Point{Latitude: *restaurant.Latitude, Longitude: *restaurant.Longitude}
	}
	result := QuoteResult{Items: menu, PickupAddress: restaurant.Address, RestaurantLocation: location, DeliveryAvailable: true}
	quantities := make(map[int64]int64, len(items))
	for _, i := range items {
		quantities[i.MenuItemID] = i.Quantity
	}
	for _, i := range menu {
		q := quantities[i.ID]
		if q <= 0 {
			return QuoteResult{}, ErrInvalidQuote
		}
		if i.Currency != domain.CurrencyTJS || i.PriceMinor <= 0 || i.PriceMinor > math.MaxInt64/q {
			return QuoteResult{}, domain.ErrAmountOverflow
		}
		result.ItemsSubtotalMinor, err = domain.AddMinor(result.ItemsSubtotalMinor, i.PriceMinor*q)
		if err != nil {
			return QuoteResult{}, err
		}
	}
	result.TotalMinor = result.ItemsSubtotalMinor
	if len(options) == 0 {
		return result, nil
	}
	o := options[0]
	snapshot := &domain.PricingSnapshot{SettingsVersion: restaurant.Settings.Version, MinimumDeliverySubtotalMinor: restaurant.Settings.MinimumDeliverySubtotalMinor}
	result.PricingSnapshot = snapshot
	reason := ""
	if o.FulfillmentType == "PICKUP" {
		if !restaurant.Settings.PickupEnabled {
			reason = "pickup_disabled"
		} else if restaurant.Address == "" {
			reason = "pickup_address_missing"
		}
	} else {
		if location != nil {
			distance, distanceErr := domain.DistanceMeters(*location, *o.DeliveryLocation)
			if distanceErr != nil {
				return QuoteResult{}, distanceErr
			}
			result.DistanceMeters = &distance
			snapshot.DistanceMeters = &distance
		}
		switch {
		case !restaurant.Settings.DeliveryEnabled:
			reason = "delivery_disabled"
		case location == nil:
			reason = "restaurant_coordinates_missing"
		case len(restaurant.Tariffs) == 0:
			reason = "tariffs_missing"
		default:
			tariff, ok := domain.SelectTariff(restaurant.Tariffs, *result.DistanceMeters)
			if !ok {
				reason = "outside_delivery_zone"
			} else {
				snapshot.TariffID = tariff.ID
				snapshot.MaxDistanceMeters = tariff.MaxDistanceMeters
				snapshot.DeliveryFeeMinor = tariff.DeliveryFeeMinor
				result.DeliveryFeeMinor = tariff.DeliveryFeeMinor
				if result.ItemsSubtotalMinor < restaurant.Settings.MinimumDeliverySubtotalMinor {
					reason = "minimum_delivery_subtotal"
				}
			}
		}
	}
	result.DeliveryAvailable = reason == ""
	result.UnavailableReason = reason
	result.TotalMinor, err = domain.AddMinor(result.ItemsSubtotalMinor, result.DeliveryFeeMinor)
	return result, err
}
