package domain

import "time"

const (
	OfferPending   = "PENDING"
	OfferAccepted  = "ACCEPTED"
	OfferDeclined  = "DECLINED"
	OfferExpired   = "EXPIRED"
	OfferCancelled = "CANCELLED"
	OfferTTL       = 30 * time.Second
)

type CourierOffer struct {
	ID          int64
	DeliveryID  int64
	CourierID   int64
	Status      string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	RespondedAt *time.Time
}

func NewCourierOffer(delivery, courier int64, now, deadline time.Time) (CourierOffer, error) {
	if delivery <= 0 || courier <= 0 || now.IsZero() || !deadline.After(now) {
		return CourierOffer{}, ErrInvalid
	}
	now = now.UTC().Truncate(time.Microsecond)
	expires := now.Add(OfferTTL)
	if deadline.Before(expires) {
		expires = deadline.UTC().Truncate(time.Microsecond)
	}
	return CourierOffer{DeliveryID: delivery, CourierID: courier, Status: OfferPending, CreatedAt: now, ExpiresAt: expires}, nil
}

// Respond is idempotent for the same decision. An expired offer cannot be accepted.
func (o *CourierOffer) Respond(courier int64, accept bool, now time.Time) error {
	if courier <= 0 || courier != o.CourierID {
		return ErrNotFound
	}
	target := OfferDeclined
	if accept {
		target = OfferAccepted
	}
	if o.Status == target {
		return nil
	}
	if o.Status != OfferPending || !now.Before(o.ExpiresAt) {
		return ErrConflict
	}
	o.finish(target, now)
	return nil
}

func (o *CourierOffer) Expire(now time.Time) bool {
	if o.Status != OfferPending || now.Before(o.ExpiresAt) {
		return false
	}
	o.finish(OfferExpired, now)
	return true
}

func (o *CourierOffer) Cancel(now time.Time) {
	if o.Status == OfferPending {
		o.finish(OfferCancelled, now)
	}
}

func (o *CourierOffer) finish(status string, now time.Time) {
	now = now.UTC().Truncate(time.Microsecond)
	o.Status = status
	o.RespondedAt = &now
}
