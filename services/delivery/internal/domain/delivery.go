package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Available         = "AVAILABLE"
	Offline           = "OFFLINE"
	Busy              = "BUSY"
	PendingAssignment = "PENDING_ASSIGNMENT"
	Assigned          = "ASSIGNED"
	PickedUp          = "PICKED_UP"
	Delivered         = "DELIVERED"
	Cancelled         = "CANCELLED"
	POSOnDelivery     = "POS_ON_DELIVERY"
)

var (
	ErrInvalid            = errors.New("invalid delivery input")
	ErrTransition         = errors.New("invalid delivery state transition")
	ErrNotFound           = errors.New("resource not found")
	ErrForbidden          = errors.New("courier access denied")
	ErrConflict           = errors.New("courier or delivery already assigned")
	ErrCancellationUnsafe = errors.New("delivery cancellation requires operator intervention")
	ErrPayment            = errors.New("explicit POS payment confirmation is required")
)

type Courier struct {
	ID        int64
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func NewCourier(name string, now time.Time) (Courier, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 200 || strings.ContainsRune(name, 0) || now.IsZero() {
		return Courier{}, ErrInvalid
	}
	return Courier{Name: name, Status: Available, CreatedAt: now.UTC().Truncate(time.Microsecond), UpdatedAt: now.UTC().Truncate(time.Microsecond)}, nil
}

// Confirmation is an operational statement by the courier, not bank verification.
type Confirmation struct {
	ID               int64
	OrderID          int64
	DeliveryID       int64
	AmountMinor      int64
	Currency         string
	PaymentMethod    string
	ReceiptReference string
	ConfirmedBy      int64
	ConfirmedAt      time.Time
}
type Delivery struct {
	DeliveryAddress      string
	ID                   int64
	OrderID              int64
	CourierID            int64
	Status               string
	AmountToCollectMinor int64
	Currency             string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Version              int64
	OrderVersion         int64
	Ready                bool
	PreparationStarted   bool
	Confirmation         *Confirmation
}

func NewDelivery(order, amount int64, currency string, orderVersion int64, now time.Time) (Delivery, error) {
	if order <= 0 || amount <= 0 || currency != "TJS" || orderVersion < 2 || now.IsZero() {
		return Delivery{}, ErrInvalid
	}
	return Delivery{OrderID: order, Status: PendingAssignment, AmountToCollectMinor: amount, Currency: currency, CreatedAt: now.UTC().Truncate(time.Microsecond), UpdatedAt: now.UTC().Truncate(time.Microsecond), Version: 1, OrderVersion: orderVersion}, nil
}
func (d *Delivery) Assign(courier int64, now time.Time) error {
	if courier <= 0 {
		return ErrInvalid
	}
	if d.Status != PendingAssignment {
		return ErrConflict
	}
	d.CourierID = courier
	d.advance(Assigned, now)
	return nil
}
func (d *Delivery) Pickup(courier int64, now time.Time) error {
	if courier <= 0 || d.CourierID != courier {
		return ErrForbidden
	}
	if d.Status != Assigned || !d.Ready {
		return ErrTransition
	}
	d.advance(PickedUp, now)
	return nil
}
func (d *Delivery) Complete(courier int64, confirmed bool, reference string, now time.Time) error {
	if courier <= 0 || d.CourierID != courier {
		return ErrForbidden
	}
	if !confirmed {
		return ErrPayment
	}
	reference = strings.TrimSpace(reference)
	if !utf8.ValidString(reference) || utf8.RuneCountInString(reference) > 200 || strings.ContainsAny(reference, "\x00\r\n") {
		return ErrInvalid
	}
	if d.Status == Delivered {
		if d.Confirmation == nil || d.Confirmation.ReceiptReference != reference {
			return ErrConflict
		}
		return nil
	}
	if d.Status != PickedUp {
		return ErrTransition
	}
	d.advance(Delivered, now)
	d.Confirmation = &Confirmation{OrderID: d.OrderID, DeliveryID: d.ID, AmountMinor: d.AmountToCollectMinor, Currency: d.Currency, PaymentMethod: POSOnDelivery, ReceiptReference: reference, ConfirmedBy: courier, ConfirmedAt: d.UpdatedAt}
	return nil
}
func (d *Delivery) Cancel(now time.Time) error {
	if d.Status == Cancelled {
		return nil
	}
	if d.PreparationStarted || d.Status == PickedUp || d.Status == Delivered {
		return ErrCancellationUnsafe
	}
	if d.Status != PendingAssignment && d.Status != Assigned {
		return ErrTransition
	}
	d.advance(Cancelled, now)
	return nil
}
func (d *Delivery) advance(status string, now time.Time) {
	if now.Before(d.UpdatedAt) {
		now = d.UpdatedAt
	}
	d.Status = status
	d.Version++
	d.UpdatedAt = now.UTC().Truncate(time.Microsecond)
}

func (c *Courier) SetAvailability(status string, now time.Time) error {
	if status != Available && status != Offline {
		return ErrInvalid
	}
	if c.Status == Busy {
		return ErrConflict
	}
	c.Status = status
	if now.Before(c.UpdatedAt) {
		now = c.UpdatedAt
	}
	c.UpdatedAt = now.UTC().Truncate(time.Microsecond)
	return nil
}
func (d *Delivery) Decline(courier int64, now time.Time) error {
	if courier <= 0 || d.CourierID != courier {
		return ErrForbidden
	}
	if d.Status != Assigned {
		return ErrTransition
	}
	d.CourierID = 0
	d.advance(PendingAssignment, now)
	return nil
}
func (d *Delivery) ObservePreparation() { d.PreparationStarted = true }
func (d *Delivery) MarkReady() error {
	if !d.PreparationStarted {
		return ErrTransition
	}
	d.Ready = true
	return nil
}
func (d *Delivery) ReportState(now time.Time)        { d.advance(d.Status, now) }
func (d *Delivery) RejectCancellation(now time.Time) { d.advance(d.Status, now) }
