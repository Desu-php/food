package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

func (r *Repository) ProcessOrder(ctx context.Context, e events.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	tag, err := tx.Exec(ctx, `INSERT INTO processed_events(event_id,order_id,aggregate_version,fingerprint) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, e.EventID, e.AggregateID, e.AggregateVersion, hash[:])
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM processed_events WHERE event_id=$1 AND order_id=$2 AND aggregate_version=$3`, e.EventID, e.AggregateID, e.AggregateVersion).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(stored, hash[:]) {
			return application.ErrEventConflict
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_streams(order_id) VALUES($1) ON CONFLICT DO NOTHING`, e.AggregateID); err != nil {
		return err
	}
	var version int64
	if err = tx.QueryRow(ctx, `SELECT version FROM order_streams WHERE order_id=$1 FOR UPDATE`, e.AggregateID).Scan(&version); err != nil {
		return err
	}
	// v2 may be the first event for orders created before Outbox was introduced.
	if e.AggregateVersion != version+1 && !(version == 0 && e.AggregateVersion == 2) {
		return application.ErrEventGap
	}
	p := e.Payload
	// Retain Inbox/version fencing, but pickup has no delivery projection or effects.
	if p.FulfillmentType == "PICKUP" {
		if _, err = tx.Exec(ctx, `UPDATE order_streams SET version=$2 WHERE order_id=$1`, e.AggregateID, e.AggregateVersion); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var preparation, ready bool
	if err = tx.QueryRow(ctx, `SELECT preparation_started,ready FROM order_streams WHERE order_id=$1`, p.OrderID).Scan(&preparation, &ready); err != nil {
		return err
	}
	if e.EventType == events.OrderCancellationRequested || e.EventType == events.OrderCancelled || e.EventType == events.OrderRejected || e.EventType == events.OrderResumed {
		blocked := e.EventType != events.OrderResumed
		if _, err = tx.Exec(ctx, `UPDATE order_streams SET dispatch_blocked=$2 WHERE order_id=$1`, p.OrderID, blocked); err != nil {
			return err
		}
	}
	if e.EventType == events.OrderPreparing {
		preparation = true
	}
	if e.EventType == events.OrderReadyForPickup {
		preparation = true
		ready = true
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM deliveries WHERE order_id=$1`, p.OrderID).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		d, err := loadDelivery(ctx, tx, id)
		if err != nil {
			return err
		}
		if d.AmountToCollectMinor != p.SubtotalMinor || d.Currency != p.Currency {
			return application.ErrEventConflict
		}
		if preparation {
			d.ObservePreparation()
		}
		if ready {
			if err = d.MarkReady(); err != nil {
				return err
			}
		}
		d.OrderVersion = max(d.OrderVersion, e.AggregateVersion)
		if err = saveDelivery(ctx, tx, d); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE order_streams SET version=$2,preparation_started=$3,ready=$4 WHERE order_id=$1`, e.AggregateID, e.AggregateVersion, preparation, ready); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
