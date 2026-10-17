// Package delivery defines the public delivery and operational POS confirmation contract.
package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"example.com/food/contracts/correlation"
	orders "example.com/food/contracts/orders/v1"
)

const (
	Topic                        = "delivery.events.v1"
	DeliveryCreated              = "DeliveryCreated"
	CourierAssigned              = "CourierAssigned"
	DeliveryPickedUp             = "DeliveryPickedUp"
	DeliveryCompleted            = "DeliveryCompleted"
	DeliveryCancelled            = "DeliveryCancelled"
	CourierReleased              = "CourierReleased"
	DeliveryCancellationRejected = "DeliveryCancellationRejected"
	DeliveryStateReported        = "DeliveryStateReported"
)

var ErrInvalid = errors.New("invalid delivery event")

type Confirmation struct {
	ID            int64     `json:"id,omitempty"`
	OrderID       int64     `json:"order_id"`
	DeliveryID    int64     `json:"delivery_id"`
	AmountMinor   int64     `json:"amount_minor"`
	Currency      string    `json:"currency"`
	PaymentMethod string    `json:"payment_method"`
	POSReference  string    `json:"pos_reference,omitempty"`
	ConfirmedBy   int64     `json:"confirmed_by"`
	ConfirmedAt   time.Time `json:"confirmed_at"`
}

func (p Confirmation) Valid() bool {
	return p.ID >= 0 && p.OrderID > 0 && p.DeliveryID > 0 && p.AmountMinor > 0 && p.Currency == "TJS" && p.PaymentMethod == "POS_ON_DELIVERY" && p.ConfirmedBy > 0 && !p.ConfirmedAt.IsZero() && utf8.ValidString(p.POSReference) && utf8.RuneCountInString(p.POSReference) <= 200 && !strings.ContainsAny(p.POSReference, "\x00\r\n")
}

type Payload struct {
	Status               string        `json:"status,omitempty"`
	PreparationStarted   bool          `json:"preparation_started,omitempty"`
	Reason               string        `json:"reason,omitempty"`
	OrderID              int64         `json:"order_id"`
	CourierID            int64         `json:"courier_id,omitempty"`
	AmountToCollectMinor int64         `json:"amount_to_collect_minor"`
	Currency             string        `json:"currency"`
	Confirmation         *Confirmation `json:"confirmation,omitempty"`
}
type Event struct {
	CausationID      string    `json:"causation_id,omitempty"`
	EventID          string    `json:"event_id"`
	EventType        string    `json:"event_type"`
	SchemaVersion    int       `json:"schema_version"`
	AggregateID      int64     `json:"aggregate_id"`
	AggregateVersion int64     `json:"aggregate_version"`
	CorrelationID    string    `json:"correlation_id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Payload          Payload   `json:"payload"`
}

func (e Event) Validate() error {
	p := e.Payload
	if e.CausationID != "" && !orders.ValidID(e.CausationID) {
		return ErrInvalid
	}
	if !orders.ValidID(e.EventID) || e.SchemaVersion != 1 || e.AggregateID <= 0 || e.AggregateVersion < 1 || !correlation.Valid(e.CorrelationID) || e.OccurredAt.IsZero() || p.OrderID <= 0 || p.AmountToCollectMinor <= 0 || p.Currency != "TJS" {
		return ErrInvalid
	}
	switch e.EventType {
	case DeliveryCreated:
		if e.AggregateVersion != 1 || p.CourierID != 0 {
			return ErrInvalid
		}
	case CourierAssigned, DeliveryPickedUp, DeliveryCompleted:
		if e.AggregateVersion < 2 || p.CourierID <= 0 {
			return ErrInvalid
		}
	case DeliveryCancelled, CourierReleased, DeliveryCancellationRejected, DeliveryStateReported:
		if e.AggregateVersion < 2 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if e.EventType == DeliveryStateReported && p.Status != "PENDING_ASSIGNMENT" && p.Status != "ASSIGNED" && p.Status != "PICKED_UP" && p.Status != "DELIVERED" && p.Status != "CANCELLED" {
		return ErrInvalid
	}
	if e.EventType == DeliveryCompleted || e.EventType == DeliveryStateReported && p.Status == "DELIVERED" {
		if p.Confirmation == nil || !p.Confirmation.Valid() || p.Confirmation.OrderID != p.OrderID || p.Confirmation.DeliveryID != e.AggregateID || p.Confirmation.AmountMinor != p.AmountToCollectMinor || p.Confirmation.Currency != p.Currency || p.Confirmation.ConfirmedBy != p.CourierID || e.EventType == DeliveryCompleted && !p.Confirmation.ConfirmedAt.Equal(e.OccurredAt) {
			return ErrInvalid
		}
	} else if p.Confirmation != nil {
		return ErrInvalid
	}
	return nil
}
func Decode(body []byte) (Event, error) {
	var e Event
	if len(body) > orders.MaxMessageBytes || json.Unmarshal(body, &e) != nil {
		return e, ErrInvalid
	}
	return e, e.Validate()
}
