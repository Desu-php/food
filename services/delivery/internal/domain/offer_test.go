package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCourierOfferTTLAndDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, window := range []time.Duration{time.Minute, 5 * time.Second} {
		o, err := NewCourierOffer(1, 2, now, now.Add(window))
		if err != nil || o.Status != OfferPending || o.ExpiresAt.Sub(now) != min(window, OfferTTL) || o.RespondedAt != nil {
			t.Fatal(o, err)
		}
	}
	for _, ids := range [][2]int64{{0, 2}, {1, 0}} {
		if _, err := NewCourierOffer(ids[0], ids[1], now, now.Add(time.Minute)); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := NewCourierOffer(1, 2, now, now); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestCourierOfferDecisions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, accept := range []bool{false, true} {
		o, _ := NewCourierOffer(1, 2, now, now.Add(time.Minute))
		if err := o.Respond(3, accept, now); !errors.Is(err, ErrNotFound) || o.Status != OfferPending {
			t.Fatal(o, err)
		}
		if err := o.Respond(2, accept, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		original := *o.RespondedAt
		if err := o.Respond(2, accept, now.Add(time.Hour)); err != nil || !o.RespondedAt.Equal(original) {
			t.Fatal(o, err)
		}
		if err := o.Respond(2, !accept, now.Add(time.Second)); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if o.Expire(now.Add(time.Hour)) {
			t.Fatal("terminal offer expired")
		}
		o.Cancel(now.Add(time.Hour))
		if !o.RespondedAt.Equal(original) {
			t.Fatal("terminal audit changed")
		}
	}
}

func TestCourierOfferExpiryAndCancellation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	o, _ := NewCourierOffer(1, 2, now, now.Add(time.Minute))
	if o.Expire(o.ExpiresAt.Add(-time.Microsecond)) {
		t.Fatal("expired early")
	}
	if err := o.Respond(2, true, o.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if !o.Expire(o.ExpiresAt) || o.Status != OfferExpired || o.RespondedAt == nil {
		t.Fatal(o)
	}
	if o.Expire(o.ExpiresAt.Add(time.Second)) {
		t.Fatal("expired twice")
	}
	o, _ = NewCourierOffer(1, 2, now, now.Add(time.Minute))
	o.Cancel(now.Add(time.Second))
	if o.Status != OfferCancelled {
		t.Fatal(o)
	}
	if err := o.Respond(2, true, now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
