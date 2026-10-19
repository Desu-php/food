package postgres

import (
	"context"
	"encoding/json"
	"github.com/Desu-php/food/contracts/correlation"
	offers "github.com/Desu-php/food/contracts/courieroffers/v1"
	ids "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/domain"
	"github.com/jackc/pgx/v5"
)

func insertOfferEvent(ctx context.Context, tx pgx.Tx, o domain.CourierOffer) error {
	var order int64
	if err := tx.QueryRow(ctx, `SELECT order_id FROM deliveries WHERE id=$1`, o.DeliveryID).Scan(&order); err != nil {
		return err
	}
	kind := offers.CourierOfferCreated
	version := o.ID * 2
	occurred := o.CreatedAt
	if o.Status != domain.OfferPending {
		kind = offers.CourierOfferClosed
		version++
		if o.RespondedAt != nil {
			occurred = *o.RespondedAt
		}
	}
	e := offers.Event{EventID: ids.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: o.DeliveryID, AggregateVersion: version, CorrelationID: correlation.ID(ctx), OccurredAt: occurred, Payload: offers.Payload{OfferID: o.ID, DeliveryID: o.DeliveryID, OrderID: order, CourierID: o.CourierID, Status: o.Status, ExpiresAt: o.ExpiresAt}}
	if e.CorrelationID == "" {
		e.CorrelationID = e.EventID
	}
	if err := e.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,aggregate_version,event_type,correlation_id,body,stream) VALUES($1,$2,$3,$4,$5,$6,'courier_offers')`, e.EventID, e.AggregateID, e.AggregateVersion, e.EventType, e.CorrelationID, body)
	return err
}
func (r *Repository) OfferLive(ctx context.Context, offer, courier int64) (bool, error) {
	var live bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM courier_offers o JOIN deliveries d ON d.id=o.delivery_id JOIN couriers c ON c.id=o.courier_id WHERE o.id=$1 AND o.courier_id=$2 AND o.status='PENDING' AND o.expires_at>clock_timestamp() AND d.status='PENDING_ASSIGNMENT' AND c.status='AVAILABLE' AND d.dispatch_deadline>clock_timestamp() AND NOT d.preparation_started AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=d.order_id AND s.dispatch_blocked) AND NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL))`, offer, courier).Scan(&live)
	return live, err
}
