package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func priced(id, price, quantity int64) PricedItem {
	return PricedItem{MenuItemID: id, RestaurantID: 1, Name: "Плов", Quantity: quantity, UnitPriceMinor: price, Currency: CurrencyTJS}
}
func TestOrderInvariants(t *testing.T) {
	for _, tt := range []struct {
		name  string
		items []PricedItem
		err   error
	}{
		{"one item", []PricedItem{priced(1, 6500, 2)}, nil},
		{"several items", []PricedItem{priced(1, 6500, 2), priced(2, 100, 3)}, nil},
		{"empty", nil, ErrInvalidOrder},
		{"zero quantity", []PricedItem{priced(1, 100, 0)}, ErrInvalidItem},
		{"negative quantity", []PricedItem{priced(1, 100, -1)}, ErrInvalidItem},
		{"quantity limit", []PricedItem{priced(1, 100, 101)}, ErrInvalidItem},
		{"zero price", []PricedItem{priced(1, 0, 1)}, ErrInvalidItem},
		{"duplicate", []PricedItem{priced(1, 100, 1), priced(1, 100, 1)}, ErrInvalidItem},
		{"multiply overflow", []PricedItem{priced(1, math.MaxInt64, 2)}, ErrOverflow},
		{"sum overflow", []PricedItem{priced(1, math.MaxInt64, 1), priced(2, 1, 1)}, ErrOverflow},
		{"max amount", []PricedItem{priced(1, math.MaxInt64, 1)}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := NewOrder(1, 1, CurrencyTJS, tt.items, time.Now(), Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"})
			if !errors.Is(err, tt.err) {
				t.Fatalf("%v, want %v", err, tt.err)
			}
			if err == nil && o.Snapshot().Status != WaitingRestaurant {
				t.Fatal("wrong initial status")
			}
		})
	}
	foreign := priced(1, 100, 1)
	foreign.RestaurantID = 2
	if _, err := NewOrder(1, 1, CurrencyTJS, []PricedItem{foreign}, time.Now(), Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"}); !errors.Is(err, ErrRestaurantMismatch) {
		t.Fatal(err)
	}
	if _, err := NewOrder(1, 1, "USD", []PricedItem{priced(1, 100, 1)}, time.Now(), Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"}); !errors.Is(err, ErrInvalidOrder) {
		t.Fatal(err)
	}
}
func TestOrderSnapshotAndCancellation(t *testing.T) {
	now := time.Now()
	input := []PricedItem{priced(1, 6500, 2), priced(2, 100, 3)}
	order, err := NewOrder(7, 1, CurrencyTJS, input, now, Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"})
	if err != nil {
		t.Fatal(err)
	}
	input[0].Name = "changed"
	state := order.Snapshot()
	if state.SubtotalMinor != 13300 || state.Items[0].Name != "Плов" || state.Items[0].UnitPriceMinor != 6500 || state.Items[0].TotalPriceMinor != 13000 {
		t.Fatalf("bad snapshot: %+v", state)
	}
	state.Items[0].Name = "mutated"
	if order.Snapshot().Items[0].Name != "Плов" {
		t.Fatal("snapshot leaked mutable state")
	}
	if err := order.Cancel(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if order.Snapshot().Status != Cancelled || !order.Snapshot().UpdatedAt.After(now) {
		t.Fatal("cancellation not applied")
	}
	if err := order.Cancel(now); !errors.Is(err, ErrCannotCancel) {
		t.Fatalf("repeat: %v", err)
	}
}
func TestRestoreRejectsCorruption(t *testing.T) {
	o, _ := NewOrder(1, 1, CurrencyTJS, []PricedItem{priced(1, 100, 2)}, time.Now(), Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"})
	s := o.Snapshot()
	s.ID = 1
	s.Items[0].ID = 1
	s.Items[0].OrderID = 1
	if _, err := Restore(s); err != nil {
		t.Fatal(err)
	}
	s.Items[0].TotalPriceMinor++
	if _, err := Restore(s); !errors.Is(err, ErrInvalidOrder) {
		t.Fatal("accepted corrupt total", err)
	}
}
