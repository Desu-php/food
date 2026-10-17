package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func task(t *testing.T) Delivery {
	t.Helper()
	d, err := NewDelivery(42, 25000, "TJS", 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.ID = 1
	return d
}
func TestFulfillmentAndOperationalPOS(t *testing.T) {
	d := task(t)
	now := time.Now()
	if err := d.Complete(1, true, "", now); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := d.Assign(7, now); err != nil {
		t.Fatal(err)
	}
	if err := d.Assign(8, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := d.Pickup(7, now); !errors.Is(err, ErrTransition) {
		t.Fatal("picked up before readiness", err)
	}
	d.Ready = true
	if err := d.Pickup(8, now); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := d.Pickup(7, now); err != nil {
		t.Fatal(err)
	}
	if err := d.Complete(7, false, "", now); !errors.Is(err, ErrPayment) {
		t.Fatal(err)
	}
	if d.Status != PickedUp || d.Confirmation != nil {
		t.Fatal("unconfirmed payment changed state")
	}
	for _, ref := range []string{strings.Repeat("a", 201), "receipt\ninvalid"} {
		if err := d.Complete(7, true, ref, now); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if err := d.Complete(7, true, " POS-42 ", now); err != nil {
		t.Fatal(err)
	}
	if d.Status != Delivered || d.Version != 4 || d.Confirmation.AmountMinor != 25000 || d.Confirmation.Currency != "TJS" || d.Confirmation.ConfirmedBy != 7 || d.Confirmation.PaymentMethod != POSOnDelivery {
		t.Fatal(d)
	}
	original := *d.Confirmation
	version := d.Version
	if err := d.Complete(7, true, "POS-42", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if *d.Confirmation != original || d.Version != version {
		t.Fatal("duplicate confirmation mutated state")
	}
	if err := d.Complete(7, true, "other", now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := d.Complete(8, true, "POS-42", now); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := d.Cancel(now); !errors.Is(err, ErrCancellationUnsafe) {
		t.Fatal(err)
	}
}
func TestCancelAndValidation(t *testing.T) {
	d := task(t)
	if err := d.Cancel(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.Assign(1, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for _, name := range []string{"", strings.Repeat("a", 201), "bad\x00"} {
		if _, err := NewCourier(name, time.Now()); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if c, err := NewCourier(" Курьер ", time.Now()); err != nil || c.Name != "Курьер" || c.Status != Available {
		t.Fatal(c, err)
	}
	if _, err := NewDelivery(1, 0, "TJS", 2, time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestDeclineAndCourierAvailability(t *testing.T) {
	c, err := NewCourier("Courier", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SetAvailability(Offline, time.Now()); err != nil || c.Status != Offline {
		t.Fatal(c, err)
	}
	c.Status = Busy
	if err = c.SetAvailability(Available, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal("busy courier changed availability", err)
	}
	d := task(t)
	if err = d.Assign(7, time.Now()); err != nil {
		t.Fatal(err)
	}
	d.ObservePreparation()
	if err = d.Cancel(time.Now()); !errors.Is(err, ErrCancellationUnsafe) {
		t.Fatal(err)
	}
	if err = d.Decline(7, time.Now()); err != nil || d.Status != PendingAssignment || d.CourierID != 0 || !d.PreparationStarted {
		t.Fatal(d, err)
	}
}
