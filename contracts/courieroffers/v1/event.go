// Package courieroffers defines versioned business facts independently of service domains.
package courieroffers

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	ids "github.com/Desu-php/food/contracts/orders/v1"
)

const Topic = "courier-offers.events.v1"
const CourierOfferCreated = "CourierOfferCreated"
const CourierOfferClosed = "CourierOfferClosed"

var ErrInvalid = errors.New("invalid courieroffers event")

type Payload struct {
	OfferID    int64     `json:"offer_id"`
	DeliveryID int64     `json:"delivery_id"`
	OrderID    int64     `json:"order_id"`
	CourierID  int64     `json:"courier_id"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expires_at"`
}
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

func (e Event) Validate() error {
	p := e.Payload
	if !ids.ValidID(e.EventID) || e.SchemaVersion != 1 || e.AggregateID <= 0 || e.AggregateVersion < 1 || !correlation.Valid(e.CorrelationID) || e.OccurredAt.IsZero() || (e.EventType != CourierOfferCreated && e.EventType != CourierOfferClosed) || p.OfferID <= 0 || p.DeliveryID != e.AggregateID || p.DeliveryID <= 0 || p.OrderID <= 0 || p.CourierID <= 0 || p.ExpiresAt.IsZero() || (e.EventType == CourierOfferCreated && p.Status != "PENDING") || (e.EventType == CourierOfferClosed && p.Status != "ACCEPTED" && p.Status != "DECLINED" && p.Status != "EXPIRED" && p.Status != "CANCELLED") {
		return ErrInvalid
	}
	return nil
}
func Decode(body []byte) (Event, error) {
	var e Event
	if len(body) > ids.MaxMessageBytes || json.Unmarshal(body, &e) != nil {
		return e, ErrInvalid
	}
	return e, e.Validate()
}
