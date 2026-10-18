package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Desu-php/food/services/orders/internal/domain"
)

type catalogFunc func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error)

func (f catalogFunc) Quote(ctx context.Context, id int64, p []RequestedItem) (CatalogQuote, error) {
	items, err := f(ctx, id, p)
	return CatalogQuote{Items: items, PickupAddress: "Restaurant location"}, err
}

type repositoryStub struct {
	previous domain.Order
	found    bool
	hash     string
	saved    int
}

func (r *repositoryStub) FindCreation(_ context.Context, _ int64, _ string, hash string) (domain.Order, bool, error) {
	if r.found && r.hash != hash {
		return domain.Order{}, false, ErrIdempotencyConflict
	}
	return r.previous, r.found, nil
}
func (r *repositoryStub) Create(_ context.Context, _ string, hash string, o domain.Order) (domain.Order, error) {
	r.saved++
	r.previous = o
	r.found = true
	r.hash = hash
	return o, nil
}
func (r *repositoryStub) Get(context.Context, int64, int64) (domain.Order, error) {
	return r.previous, nil
}
func (r *repositoryStub) Cancel(_ context.Context, _ int64, _ int64, fn FulfillmentMutation) (domain.Order, error) {
	saga := domain.NewSaga(r.previous.Snapshot().ID, time.Now())
	_, err := fn(&r.previous, &saga)
	return r.previous, err
}
func command() CreateCommand {
	return CreateCommand{FulfillmentType: domain.Delivery, DeliveryAddress: "Test delivery address", CustomerID: 7, RestaurantID: 1, Currency: "TJS", ExpectedSubtotalMinor: 13000, Items: []RequestedItem{{MenuItemID: 1, Quantity: 2}}}
}
func TestCreateOrderReplayBeforeCatalog(t *testing.T) {
	repo := &repositoryStub{}
	calls := 0
	svc := NewOrders(repo, catalogFunc(func(_ context.Context, _ int64, p []RequestedItem) ([]domain.PricedItem, error) {
		calls++
		return []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Плов", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, nil
	}))
	c := command()
	first, err := svc.CreateOrder(context.Background(), c, "key")
	if err != nil {
		t.Fatal(err)
	}
	// New application instance simulates restart; persisted creation is consulted first.
	restarted := NewOrders(repo, catalogFunc(func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error) {
		t.Fatal("replay called Catalog")
		return nil, ErrCatalogUnavailable
	}))
	replay, err := restarted.CreateOrder(context.Background(), c, "key")
	if err != nil || replay.Snapshot().SubtotalMinor != first.Snapshot().SubtotalMinor || calls != 1 || repo.saved != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	c.ExpectedSubtotalMinor++
	if _, err := restarted.CreateOrder(context.Background(), c, "key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}
func TestCreateOrderFailures(t *testing.T) {
	for _, failure := range []error{ErrRestaurantNotFound, ErrItemNotFound, ErrRestaurantUnavailable, ErrItemUnavailable, ErrItemRestaurantMismatch, ErrCatalogUnavailable} {
		t.Run(failure.Error(), func(t *testing.T) {
			repo := &repositoryStub{}
			svc := NewOrders(repo, catalogFunc(func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error) { return nil, failure }))
			if _, err := svc.CreateOrder(context.Background(), command(), "key"); !errors.Is(err, failure) || repo.saved != 0 {
				t.Fatalf("%v, saved %d", err, repo.saved)
			}
		})
	}
	repo := &repositoryStub{}
	svc := NewOrders(repo, catalogFunc(func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error) {
		return []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "New name", Quantity: 2, UnitPriceMinor: 7000, Currency: "TJS"}}, nil
	}))
	if _, err := svc.CreateOrder(context.Background(), command(), "key"); !errors.Is(err, ErrPriceChanged) || repo.saved != 0 {
		t.Fatalf("price change: %v", err)
	}
	c := command()
	c.ExpectedSubtotalMinor = 14000
	o, err := svc.CreateOrder(context.Background(), c, "confirmed-price")
	if err != nil || o.Snapshot().Items[0].Name != "New name" || o.Snapshot().Items[0].UnitPriceMinor != 7000 {
		t.Fatalf("confirmation: %+v %v", o, err)
	}
}
func TestCanonicalRequest(t *testing.T) {
	c := command()
	c.Items = append(c.Items, RequestedItem{MenuItemID: 2, Quantity: 1})
	_, a, err := fingerprint(c, "key", false)
	if err != nil {
		t.Fatal(err)
	}
	c.Items[0], c.Items[1] = c.Items[1], c.Items[0]
	_, b, err := fingerprint(c, "key", false)
	if err != nil || a != b {
		t.Fatal("item ordering affects fingerprint")
	}
	for _, key := range []string{"", "bad key", "ключ", "\n"} {
		if _, _, err := fingerprint(c, key, false); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("accepted key %q", key)
		}
	}
	c.Items[0] = c.Items[1]
	if _, _, err := fingerprint(c, "key", false); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("accepted duplicate items")
	}
}

func TestConcurrentCreationWinsOverQuoteFailure(t *testing.T) {
	repo := &repositoryStub{}
	c := command()
	_, hash, err := fingerprint(c, "key", false)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewOrders(repo, catalogFunc(func(context.Context, int64, []RequestedItem) ([]domain.PricedItem, error) {
		// Another request commits after our initial lookup and before quote returns.
		previous, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Original", Quantity: 2, UnitPriceMinor: 6500, Currency: "TJS"}}, time.Now(), domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Test delivery address"})
		if err != nil {
			t.Fatal(err)
		}
		repo.previous = previous
		repo.hash = hash
		repo.found = true
		return nil, ErrCatalogUnavailable
	}))
	order, err := svc.CreateOrder(context.Background(), c, "key")
	if err != nil || order.Snapshot().Items[0].Name != "Original" || repo.saved != 0 {
		t.Fatalf("concurrent replay: %+v %v", order, err)
	}
}

func (r *repositoryStub) IssuePickupCode(context.Context, int64, int64, []byte, time.Time) error {
	return nil
}
