package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

var (
	ErrFulfillmentUnavailable = errors.New("selected fulfillment is unavailable; obtain a current quote")
	ErrInvalidRequest         = errors.New("invalid order request")
	ErrInvalidKey             = errors.New("Idempotency-Key must contain 1 to 128 printable ASCII characters without spaces")
	ErrIdempotencyConflict    = errors.New("Idempotency-Key was already used for a different request")
	ErrPriceChanged           = errors.New("order total changed; obtain a current quote and confirm expected_total_minor")
	ErrCatalogUnavailable     = errors.New("catalog temporarily unavailable")
	ErrRestaurantNotFound     = errors.New("restaurant not found")
	ErrItemNotFound           = errors.New("menu item not found")
	ErrRestaurantUnavailable  = errors.New("restaurant is not accepting orders")
	ErrItemUnavailable        = errors.New("menu item unavailable")
	ErrItemRestaurantMismatch = errors.New("menu item belongs to another restaurant")
)

type PriceChangedError struct {
	ItemsSubtotalMinor int64  `json:"items_subtotal_minor"`
	DeliveryFeeMinor   int64  `json:"delivery_fee_minor"`
	TotalMinor         int64  `json:"total_minor"`
	Currency           string `json:"currency"`
}

func (e *PriceChangedError) Error() string        { return ErrPriceChanged.Error() }
func (e *PriceChangedError) Is(target error) bool { return target == ErrPriceChanged }

type RequestedItem struct {
	MenuItemID int64 `json:"menu_item_id"`
	Quantity   int64 `json:"quantity"`
}

type CreateCommand struct {
	CustomerID            int64
	RestaurantID          int64
	FulfillmentType       domain.FulfillmentType `json:",omitempty"`
	DeliveryAddress       string                 `json:",omitempty"`
	Currency              string
	ExpectedSubtotalMinor int64
	Items                 []RequestedItem
	ExpectedTotalMinor    *int64   `json:",omitempty"`
	DeliveryLatitude      *float64 `json:",omitempty"`
	DeliveryLongitude     *float64 `json:",omitempty"`
}

type QuoteOptions struct {
	FulfillmentType  domain.FulfillmentType `json:"fulfillment_type"`
	DeliveryLocation *geo.Point             `json:"delivery_location,omitempty"`
}
type CatalogQuote struct {
	ItemsSubtotalMinor int64
	DeliveryFeeMinor   int64
	TotalMinor         int64
	DeliveryAvailable  bool
	UnavailableReason  string
	PricingSnapshot    *domain.PricingSnapshot
	RestaurantLocation *geo.Point
	Items              []domain.PricedItem
	PickupAddress      string
}

type CatalogClient interface {
	Quote(context.Context, int64, []RequestedItem, ...QuoteOptions) (CatalogQuote, error)
}

// OrderRepository owns atomic persistence operations. Create must recheck the
// customer-scoped key under a unique constraint and persist the original result
// in the same transaction as the aggregate. Cancel locks before applying change.
type OrderRepository interface {
	IssuePickupCode(context.Context, int64, int64, []byte, time.Time) error
	FindCreation(context.Context, int64, string, string) (domain.Order, bool, error)
	Create(context.Context, string, string, domain.Order) (domain.Order, error)
	Get(context.Context, int64, int64) (domain.Order, error)
	Cancel(context.Context, int64, int64, FulfillmentMutation) (domain.Order, error)
}

type Orders struct {
	repo        OrderRepository
	catalog     CatalogClient
	fulfillment *Fulfillment
}

func NewOrders(repo OrderRepository, catalog CatalogClient, fulfillment ...*Fulfillment) *Orders {
	f := NewFulfillment(DefaultSagaConfig())
	if len(fulfillment) > 0 {
		f = fulfillment[0]
	}
	return &Orders{repo: repo, catalog: catalog, fulfillment: f}
}

func fingerprint(c CreateCommand, key string, legacyReplay bool) (CreateCommand, string, error) {
	if len(key) < 1 || len(key) > 128 {
		return c, "", ErrInvalidKey
	}
	for _, ch := range key {
		if ch < 33 || ch > 126 {
			return c, "", ErrInvalidKey
		}
	}
	if !legacyReplay && (c.FulfillmentType != domain.Delivery && c.FulfillmentType != domain.Pickup || c.FulfillmentType == domain.Delivery && !domain.ValidAddress(c.DeliveryAddress) || c.FulfillmentType == domain.Pickup && c.DeliveryAddress != "") {
		return c, "", ErrInvalidRequest
	}
	if c.CustomerID <= 0 || c.RestaurantID <= 0 || c.Currency != domain.CurrencyTJS || (c.ExpectedTotalMinor == nil && c.ExpectedSubtotalMinor <= 0 || c.ExpectedTotalMinor != nil && *c.ExpectedTotalMinor <= 0) || len(c.Items) == 0 || len(c.Items) > domain.MaxItems {
		return c, "", ErrInvalidRequest
	}
	if (c.DeliveryLatitude == nil) != (c.DeliveryLongitude == nil) || c.DeliveryLatitude != nil && !(geo.Point{Latitude: *c.DeliveryLatitude, Longitude: *c.DeliveryLongitude}).Valid() {
		return c, "", ErrInvalidRequest
	}
	c.Items = slices.Clone(c.Items)
	sort.Slice(c.Items, func(i, j int) bool { return c.Items[i].MenuItemID < c.Items[j].MenuItemID })
	for i, p := range c.Items {
		if p.MenuItemID <= 0 || p.Quantity < 1 || p.Quantity > domain.MaxQuantity || (i > 0 && c.Items[i-1].MenuItemID == p.MenuItemID) {
			return c, "", ErrInvalidRequest
		}
	}
	body, err := json.Marshal(c)
	if err != nil {
		return c, "", fmt.Errorf("fingerprint request: %w", err)
	}
	sum := sha256.Sum256(body)
	return c, hex.EncodeToString(sum[:]), nil
}

func (s *Orders) CreateOrder(ctx context.Context, c CreateCommand, key string) (domain.Order, error) {
	// A historical retry is not a new creation: keep its original request hash
	// readable without making fulfillment_type optional for any new order.
	legacyReplay := c.FulfillmentType == "" && c.DeliveryAddress == ""
	c, hash, err := fingerprint(c, key, legacyReplay)
	if err != nil {
		return domain.Order{}, err
	}
	previous, found, err := s.repo.FindCreation(ctx, c.CustomerID, key, hash)
	if err != nil || found {
		if err == nil && legacyReplay && !previous.Snapshot().LegacyDeliveryAddress {
			return domain.Order{}, ErrIdempotencyConflict
		}
		return previous, err
	}
	if legacyReplay || c.ExpectedTotalMinor == nil {
		return domain.Order{}, ErrInvalidRequest
	}
	var deliveryLocation *geo.Point
	if c.FulfillmentType == domain.Delivery {
		if c.DeliveryLatitude == nil || c.DeliveryLongitude == nil {
			return domain.Order{}, ErrInvalidRequest
		}
		deliveryLocation = &geo.Point{Latitude: *c.DeliveryLatitude, Longitude: *c.DeliveryLongitude}
		if !deliveryLocation.Valid() {
			return domain.Order{}, ErrInvalidRequest
		}
	} else if c.DeliveryLatitude != nil || c.DeliveryLongitude != nil {
		return domain.Order{}, ErrInvalidRequest
	}
	// No database transaction is open while waiting for Catalog.
	quote, err := s.catalog.Quote(ctx, c.RestaurantID, c.Items, QuoteOptions{FulfillmentType: c.FulfillmentType, DeliveryLocation: deliveryLocation})
	quoted := quote.Items
	if err == nil && !quote.DeliveryAvailable {
		err = ErrFulfillmentUnavailable
	}
	if err == nil && quote.PricingSnapshot == nil {
		err = ErrCatalogUnavailable
	}
	if err == nil {
		if len(quoted) != len(c.Items) {
			err = ErrCatalogUnavailable
		} else {
			expected := make(map[int64]int64, len(c.Items))
			for _, p := range c.Items {
				expected[p.MenuItemID] = p.Quantity
			}
			for _, p := range quoted {
				if q, ok := expected[p.MenuItemID]; !ok || q != p.Quantity || p.RestaurantID != c.RestaurantID || p.Currency != c.Currency {
					err = ErrCatalogUnavailable
					break
				}
				delete(expected, p.MenuItemID)
			}
		}
	}
	var locationSnapshot *geo.Point
	if c.FulfillmentType == domain.Delivery {
		locationSnapshot = quote.RestaurantLocation
	}
	var order domain.Order
	location := ""
	if err == nil && c.FulfillmentType == domain.Pickup {
		location = quote.PickupAddress
		if !domain.ValidAddress(location) {
			err = ErrRestaurantUnavailable
		}
	}
	if err == nil {
		order, err = domain.NewOrder(c.CustomerID, c.RestaurantID, c.Currency, quoted, time.Now(), domain.Fulfillment{DeliveryLocation: deliveryLocation, DeliveryFeeMinor: quote.DeliveryFeeMinor, PricingSnapshot: quote.PricingSnapshot, RestaurantLocation: locationSnapshot, Type: c.FulfillmentType, DeliveryAddress: c.DeliveryAddress, PickupAddress: location})
	}
	if err == nil && (order.Snapshot().SubtotalMinor != quote.ItemsSubtotalMinor || order.Snapshot().TotalMinor != quote.TotalMinor) {
		err = ErrCatalogUnavailable
	}
	if err == nil && order.Snapshot().TotalMinor != *c.ExpectedTotalMinor {
		err = &PriceChangedError{ItemsSubtotalMinor: quote.ItemsSubtotalMinor, DeliveryFeeMinor: quote.DeliveryFeeMinor, TotalMinor: quote.TotalMinor, Currency: c.Currency}
	}
	if err != nil {
		// A concurrent request may have succeeded while this request was quoting.
		previous, found, lookupErr := s.repo.FindCreation(ctx, c.CustomerID, key, hash)
		if lookupErr != nil {
			return domain.Order{}, lookupErr
		}
		if found {
			return previous, nil
		}
		return domain.Order{}, err
	}
	return s.repo.Create(ctx, key, hash, order)
}

func (s *Orders) GetOrder(ctx context.Context, customerID, id int64) (domain.Order, error) {
	if customerID <= 0 || id <= 0 {
		return domain.Order{}, ErrInvalidRequest
	}
	return s.repo.Get(ctx, customerID, id)
}
func (s *Orders) CancelOrder(ctx context.Context, customerID, id int64) (domain.Order, error) {
	if customerID <= 0 || id <= 0 {
		return domain.Order{}, ErrInvalidRequest
	}
	return s.repo.Cancel(ctx, customerID, id, func(o *domain.Order, saga *domain.Saga) (FulfillmentChange, error) {
		return s.fulfillment.Cancel(ctx, o, saga, "customer_cancelled", time.Now())
	})
}
