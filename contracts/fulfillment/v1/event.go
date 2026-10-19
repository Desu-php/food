// Package fulfillment defines versioned business facts independently of service domains.
package fulfillment

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	ids "github.com/Desu-php/food/contracts/orders/v1"
)

const Topic = "fulfillment.events.v1"
const OrderNeedsAttention = "OrderNeedsAttention"

var ErrInvalid = errors.New("invalid fulfillment event")

type Payload struct {
	OrderID      int64 `json:"order_id"`
	RestaurantID int64 `json:"restaurant_id"`
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
	if !ids.ValidID(e.EventID) || e.SchemaVersion != 1 || e.AggregateID <= 0 || e.AggregateVersion < 1 || !correlation.Valid(e.CorrelationID) || e.OccurredAt.IsZero() || (e.EventType != OrderNeedsAttention) || p.OrderID != e.AggregateID || p.OrderID <= 0 || p.RestaurantID <= 0 {
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
