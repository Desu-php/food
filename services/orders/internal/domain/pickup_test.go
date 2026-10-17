package domain

import (
	"errors"
	"testing"
	"time"
)

func pickup(t *testing.T) Order {
	t.Helper()
	o, err := NewOrder(7, 1, CurrencyTJS, []PricedItem{priced(1, 6500, 2)}, time.Now(), Fulfillment{Type: Pickup, PickupAddress: "Restaurant location"})
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func TestPickupStateMachineAndPayment(t *testing.T) {
	o := pickup(t)
	now := time.Now()
	if s := o.Snapshot(); s.PaymentMethod != POSAtPickup || s.PaymentStatus != Unpaid || s.DeliveryFeeMinor != 0 {
		t.Fatal(s)
	}
	if err := o.CompletePickup(true, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("unready completion", err)
	}
	for _, target := range []Status{Accepted, Preparing, ReadyForPickup} {
		if err := o.Transition(target, now); err != nil {
			t.Fatal(target, err)
		}
	}
	for _, fn := range []func() error{
		func() error { return o.AssignCourier(1, now) }, func() error { return o.AwaitCourier(now) }, func() error { return o.Transition(OnTheWay, now) },
		func() error { return o.Complete(13000, CurrencyTJS, POSOnDelivery, now) }, func() error { return o.Cancel(now) },
	} {
		if err := fn(); err == nil {
			t.Fatal("pickup accepted delivery operation or late cancellation")
		}
	}
	if err := o.CompletePickup(false, now); !errors.Is(err, ErrPaymentMismatch) {
		t.Fatal(err)
	}
	if o.Snapshot().PaymentStatus != Unpaid || o.Snapshot().Status != ReadyForPickup {
		t.Fatal("partial completion")
	}
	if err := o.CompletePickup(true, now); err != nil {
		t.Fatal(err)
	}
	if o.Snapshot().Status != PickedUpByCustomer || o.Snapshot().PaymentStatus != ReportedCollected {
		t.Fatal(o.Snapshot())
	}
	if err := o.CompletePickup(true, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
}
func TestPickupCancellationBeforeCooking(t *testing.T) {
	for _, accept := range []bool{false, true} {
		o := pickup(t)
		if accept {
			if err := o.Transition(Accepted, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Cancel(time.Now()); err != nil {
			t.Fatal(err)
		}
		if o.Snapshot().Status != Cancelled || o.Snapshot().CancellationRequested {
			t.Fatal(o.Snapshot())
		}
	}
}
func TestFulfillmentValidation(t *testing.T) {
	for _, f := range []Fulfillment{{}, {Type: Delivery}, {Type: Pickup}, {Type: Pickup, PickupAddress: "Restaurant", DeliveryAddress: "Client"}, {Type: Delivery, DeliveryAddress: "  "}, {Type: Delivery, DeliveryAddress: "Address", PickupAddress: "Restaurant"}} {
		if _, err := NewOrder(7, 1, CurrencyTJS, []PricedItem{priced(1, 1, 1)}, time.Now(), f); err == nil {
			t.Fatal("invalid fulfillment accepted", f)
		}
	}
	o := pickup(t)
	s := o.Snapshot()
	s.ID = 1
	s.Items[0].ID = 1
	s.Items[0].OrderID = 1
	for _, mutate := range []func(*State){func(s *State) { s.CourierID = 1 }, func(s *State) { s.DeliveryFeeMinor = 100 }, func(s *State) { s.PaymentMethod = POSOnDelivery }, func(s *State) { s.Status = Delivered; s.PaymentStatus = ReportedCollected; s.Version = 6 }} {
		bad := s
		mutate(&bad)
		if _, err := Restore(bad); err == nil {
			t.Fatal("invalid pickup restored", bad)
		}
	}
}
