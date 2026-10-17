package domain

import (
	"errors"
	"testing"
	"time"
)

func TestRestaurantDeliveryLifecycle(t *testing.T) {
	now := time.Now()
	o, err := NewOrder(1, 1, "TJS", []PricedItem{priced(1, 100, 2)}, now, Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"})
	if err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Status != WaitingRestaurant || s.PaymentMethod != POSOnDelivery || s.PaymentStatus != Unpaid {
		t.Fatal(s)
	}
	if err = o.Transition(Preparing, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
	for _, target := range []Status{Accepted, Preparing, ReadyForPickup, OnTheWay} {
		if target == Preparing {
			if err = o.AwaitCourier(now); err != nil {
				t.Fatal(err)
			}
			if err = o.AssignCourier(5, now); err != nil {
				t.Fatal(err)
			}
		}
		if err = o.Transition(target, now); err != nil {
			t.Fatal(target, err)
		}
		if err = o.Transition(target, now); !errors.Is(err, ErrInvalidTransition) {
			t.Fatal("repeat transition", err)
		}
		if err = o.Cancel(now); !errors.Is(err, ErrCannotCancel) {
			t.Fatal("customer cancelled accepted order", err)
		}
	}
	if err = o.Transition(Delivered, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("delivered without payment", err)
	}
	if err = o.Complete(199, "TJS", POSOnDelivery, now); !errors.Is(err, ErrPaymentMismatch) {
		t.Fatal(err)
	}
	if err = o.Complete(200, "TJS", POSOnDelivery, now); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Status != Delivered || s.PaymentStatus != ReportedCollected || s.Version != 8 {
		t.Fatal(s)
	}
	if err = o.Complete(200, "TJS", POSOnDelivery, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
}
func TestRejectedCannotPrepareAndHistoricalResponse(t *testing.T) {
	now := time.Now()
	o, _ := NewOrder(1, 1, "TJS", []PricedItem{priced(1, 100, 1)}, now, Fulfillment{Type: Delivery, DeliveryAddress: "Test delivery address"})
	if err := o.Transition(Cancelled, now); err != nil {
		t.Fatal(err)
	}
	if err := o.Transition(Preparing, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
	s := o.Snapshot()
	s.ID = 1
	s.Status = PendingPayment
	s.Version = 1
	s.PaymentMethod = ""
	s.PaymentStatus = ""
	s.Items[0].ID = 1
	s.Items[0].OrderID = 1
	restored, err := Restore(s)
	if err != nil || restored.Snapshot().Status != PendingPayment {
		t.Fatal("historical response unreadable", err)
	}
}
