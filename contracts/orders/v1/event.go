// Package orders defines the public Orders integration contract, independent of domains.
package orders

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
)

const (
	Topic                      = "orders.events.v1"
	OrderPickedUpByCustomer    = "OrderPickedUpByCustomer"
	OrderCreated               = "OrderCreated"
	OrderCancelled             = "OrderCancelled"
	OrderAccepted              = "OrderAccepted"
	OrderRejected              = "OrderRejected"
	OrderPreparing             = "OrderPreparing"
	OrderReadyForPickup        = "OrderReadyForPickup"
	OrderOnTheWay              = "OrderOnTheWay"
	OrderDelivered             = "OrderDelivered"
	OrderWaitingCourier        = "OrderWaitingCourier"
	OrderCourierAssigned       = "OrderCourierAssigned"
	OrderCourierReleased       = "OrderCourierReleased"
	OrderCancellationRequested = "OrderCancellationRequested"
	OrderResumed               = "OrderResumed"
	MaxMessageBytes            = 64 << 10
)

var (
	ErrInvalid     = errors.New("invalid order event")
	ErrUnsupported = errors.New("unsupported order event type or schema version")
)

type Event struct {
	EventID          string    `json:"event_id"`
	EventType        string    `json:"event_type"`
	SchemaVersion    int       `json:"schema_version"`
	AggregateID      int64     `json:"aggregate_id"`
	AggregateVersion int64     `json:"aggregate_version"`
	CorrelationID    string    `json:"correlation_id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Payload          Payload   `json:"payload"`
}

// IDs retain the existing services' positive int64 representation.
type Payload struct {
	DeliveryFeeMinor int64  `json:"delivery_fee_minor,omitempty"`
	TotalMinor       int64  `json:"total_minor,omitempty"`
	FulfillmentType  string `json:"fulfillment_type,omitempty"`
	OrderID          int64  `json:"order_id"`
	CustomerID       int64  `json:"customer_id"`
	RestaurantID     int64  `json:"restaurant_id"`
	SubtotalMinor    int64  `json:"subtotal_minor"`
	Currency         string `json:"currency"`
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func ValidID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (e Event) Validate() error {
	if e.SchemaVersion != 1 || !KnownType(e.EventType) {
		return ErrUnsupported
	}
	p := e.Payload
	if p.DeliveryFeeMinor < 0 || p.TotalMinor < 0 || p.TotalMinor != 0 && (p.TotalMinor < p.SubtotalMinor || p.TotalMinor-p.SubtotalMinor != p.DeliveryFeeMinor) || p.TotalMinor == 0 && p.DeliveryFeeMinor != 0 || p.FulfillmentType == "PICKUP" && p.DeliveryFeeMinor != 0 {
		return ErrInvalid
	}
	if p.FulfillmentType != "" && p.FulfillmentType != "DELIVERY" && p.FulfillmentType != "PICKUP" || e.EventType == OrderPickedUpByCustomer && p.FulfillmentType != "PICKUP" {
		return ErrInvalid
	}
	if !ValidID(e.EventID) || e.AggregateID <= 0 || p.OrderID != e.AggregateID || p.CustomerID <= 0 || p.RestaurantID <= 0 || p.SubtotalMinor <= 0 || p.Currency != "TJS" || e.OccurredAt.IsZero() || !correlation.Valid(e.CorrelationID) {
		return ErrInvalid
	}
	if e.EventType == OrderCreated && e.AggregateVersion != 1 || e.EventType != OrderCreated && e.AggregateVersion < 2 {
		return ErrInvalid
	}
	return nil
}

func Decode(body []byte) (Event, error) {
	var e Event
	if len(body) > MaxMessageBytes {
		return e, ErrInvalid
	}
	// Unknown additive fields are deliberately accepted for forward compatibility.
	if err := json.Unmarshal(body, &e); err != nil {
		return e, ErrInvalid
	}
	return e, e.Validate()
}

func KnownType(kind string) bool {
	switch kind {
	case OrderPickedUpByCustomer, OrderCreated, OrderCancelled, OrderAccepted, OrderRejected, OrderPreparing, OrderReadyForPickup, OrderOnTheWay, OrderDelivered, OrderWaitingCourier, OrderCourierAssigned, OrderCourierReleased, OrderCancellationRequested, OrderResumed:
		return true
	}
	return false
}

// AmountToCollect preserves the agreed value of historical zero-fee events.
func (p Payload) AmountToCollect() int64 {
	if p.TotalMinor == 0 {
		return p.SubtotalMinor
	}
	return p.TotalMinor
}
