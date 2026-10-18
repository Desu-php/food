package application

import (
	"context"
	"encoding/json"

	"github.com/Desu-php/food/contracts/correlation"
	events "github.com/Desu-php/food/contracts/delivery/v1"
	orders "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func DeliveryEvent(ctx context.Context, d domain.Delivery, kind string) events.Event {
	id := correlation.ID(ctx)
	if !correlation.Valid(id) {
		id = orders.NewID()
	}
	e := events.Event{EventID: orders.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: d.ID, AggregateVersion: d.Version, CorrelationID: id, OccurredAt: d.UpdatedAt, Payload: events.Payload{OrderID: d.OrderID, CourierID: d.CourierID, AmountToCollectMinor: d.AmountToCollectMinor, Currency: d.Currency}}
	if kind == events.DeliveryStateReported {
		e.Payload.Status = d.Status
		e.Payload.PreparationStarted = d.PreparationStarted
	}
	if d.Confirmation != nil && (kind == events.DeliveryCompleted || kind == events.DeliveryStateReported) {
		p := d.Confirmation
		e.Payload.Confirmation = &events.Confirmation{ID: p.ID, OrderID: p.OrderID, DeliveryID: p.DeliveryID, AmountMinor: p.AmountMinor, Currency: p.Currency, PaymentMethod: p.PaymentMethod, POSReference: p.ReceiptReference, ConfirmedBy: p.ConfirmedBy, ConfirmedAt: p.ConfirmedAt}
	}
	return e
}
func EncodeEvent(ctx context.Context, d domain.Delivery, kind string) (events.Event, []byte, error) {
	e := DeliveryEvent(ctx, d, kind)
	if err := e.Validate(); err != nil {
		return e, nil, err
	}
	body, err := json.Marshal(e)
	return e, body, err
}
