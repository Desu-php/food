package application

import (
	"context"

	"github.com/Desu-php/food/contracts/correlation"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

// OrderEvent maps an aggregate snapshot to the public integration contract.
func OrderEvent(ctx context.Context, s domain.State, kind string) events.Event {
	id := correlation.ID(ctx)
	if !correlation.Valid(id) {
		id = events.NewID()
	}
	return events.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1,
		AggregateID: s.ID, AggregateVersion: s.Version, CorrelationID: id, OccurredAt: s.UpdatedAt,
		Payload: events.Payload{FulfillmentType: string(s.FulfillmentType), OrderID: s.ID, CustomerID: s.CustomerID, RestaurantID: s.RestaurantID, SubtotalMinor: s.SubtotalMinor, Currency: s.Currency}}
}
