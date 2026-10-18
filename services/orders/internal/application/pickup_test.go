package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestPickupCreateAndIdempotency(t *testing.T) {
	repo := &repositoryStub{}
	c := command()
	c.FulfillmentType = domain.Pickup
	c.DeliveryAddress = ""
	svc := NewOrders(repo, catalogFunc(func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error) {
		return []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Dish", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, nil
	}))
	o, err := svc.CreateOrder(context.Background(), c, "pickup")
	if err != nil {
		t.Fatal(err)
	}
	s := o.Snapshot()
	if s.FulfillmentType != domain.Pickup || s.DeliveryFeeMinor != 0 || s.PickupAddress != "Restaurant location" || s.PaymentMethod != domain.POSAtPickup {
		t.Fatal(s)
	}
	svc = NewOrders(repo, nil)
	if _, err := svc.CreateOrder(context.Background(), c, "pickup"); err != nil {
		t.Fatal("replay called catalog", err)
	}
	c.FulfillmentType = domain.Delivery
	c.DeliveryAddress = "Delivery address"
	if _, err := svc.CreateOrder(context.Background(), c, "pickup"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}
func TestPickupDoesNotStartOrCompensateSaga(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	f := NewFulfillment(DefaultSagaConfig())
	makeOrder := func() domain.Order {
		o, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Dish", Quantity: 1, UnitPriceMinor: 100, Currency: "TJS"}}, now, domain.Fulfillment{Type: domain.Pickup, PickupAddress: "Restaurant"})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	for _, accepted := range []bool{false, true} {
		o := makeOrder()
		s := domain.Saga{}
		if accepted {
			c, err := f.Accept(ctx, &o, &s, now)
			if err != nil || c.EventKind != events.OrderAccepted || len(c.Commands) != 0 || s.OrderID != 0 {
				t.Fatal(c, s, err)
			}
		}
		c, err := f.Cancel(ctx, &o, &s, "customer_cancelled", now)
		if err != nil || len(c.Commands) != 0 || c.ReplayCommandID != "" || o.Snapshot().Status != domain.Cancelled || s.OrderID != 0 {
			t.Fatal(c, s, err)
		}
	}
	o := makeOrder()
	s := domain.Saga{}
	if _, err := f.Accept(ctx, &o, &s, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Prepare(&o, &s, now); err != nil {
		t.Fatal("pickup needs courier", err)
	}
	if c, err := f.Ready(&o, &s, now); err != nil || c.EventKind != events.OrderReadyForPickup || len(c.Commands) != 0 {
		t.Fatal(c, err)
	}
	if _, err := f.Retry(ctx, &o, &s, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal(err)
	}
	if _, err := f.Tick(ctx, &o, &s, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal(err)
	}
}
func TestCreateRequiresExplicitFulfillmentAndDeliveryAddress(t *testing.T) {
	for _, c := range []CreateCommand{func() CreateCommand { c := command(); c.FulfillmentType = ""; return c }(), func() CreateCommand { c := command(); c.DeliveryAddress = ""; return c }(), func() CreateCommand { c := command(); c.FulfillmentType = domain.Pickup; return c }()} {
		if _, _, err := fingerprint(c, "key", false); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
}

func TestHistoricalCreationReplayWithoutFulfillment(t *testing.T) {
	ctx := context.Background()
	c := command()
	c.FulfillmentType = ""
	c.DeliveryAddress = ""
	// This is the exact pre-extension JSON field layout used in persisted hashes.
	original := `{"CustomerID":7,"RestaurantID":1,"Currency":"TJS","ExpectedSubtotalMinor":13000,"Items":[{"menu_item_id":1,"quantity":2}]}`
	sum := sha256.Sum256([]byte(original))
	hash := hex.EncodeToString(sum[:])
	// Persisted creation result from the delivery-only service, before this extension.
	current, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Original", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, time.Now(), domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Original address"})
	if err != nil {
		t.Fatal(err)
	}
	state := current.Snapshot()
	state.ID = 42
	state.FulfillmentType = ""
	state.DeliveryAddress = ""
	state.Items[0].ID = 1
	state.Items[0].OrderID = 42
	historical, err := domain.Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	repo := &repositoryStub{previous: historical, found: true, hash: hash}
	got, err := NewOrders(repo, nil).CreateOrder(ctx, c, "old")
	if err != nil || got.Snapshot().ID != 42 || !got.Snapshot().LegacyDeliveryAddress || repo.saved != 0 {
		t.Fatal("historical replay", err)
	}
	repo.found = false
	if _, err := NewOrders(repo, nil).CreateOrder(ctx, c, "new"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("created a new order without fulfillment", err)
	}
}
