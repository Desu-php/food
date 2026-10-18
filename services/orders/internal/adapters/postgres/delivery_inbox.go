package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	events "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func (r *Orders) ProcessDelivery(ctx context.Context, e events.Event) error {
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
	tag, err := tx.Exec(ctx, `INSERT INTO delivery_inbox(event_id,delivery_id,aggregate_version,fingerprint) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, e.EventID, e.AggregateID, e.AggregateVersion, hash[:])
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM delivery_inbox WHERE event_id=$1 AND delivery_id=$2 AND aggregate_version=$3`, e.EventID, e.AggregateID, e.AggregateVersion).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(stored, hash[:]) {
			return application.ErrEventConflict
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var customer int64
	err = tx.QueryRow(ctx, `SELECT customer_id FROM orders WHERE id=$1 FOR UPDATE`, e.Payload.OrderID).Scan(&customer)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	o, err := load(ctx, tx, customer, e.Payload.OrderID, false)
	if err != nil {
		return err
	}
	if o.Snapshot().FulfillmentType != domain.Delivery {
		return application.ErrEventConflict
	}
	saga, err := loadSaga(ctx, tx, e.Payload.OrderID)
	if err != nil {
		return err
	}
	if saga.DeliveryID != 0 && saga.DeliveryID != e.AggregateID {
		return application.ErrEventConflict
	}
	if e.AggregateVersion <= saga.DeliveryVersion {
		return tx.Commit(ctx)
	}
	if e.AggregateVersion != saga.DeliveryVersion+1 {
		return application.ErrEventGap
	}
	if saga.DeliveryID == 0 && e.EventType != events.DeliveryCreated {
		return application.ErrEventGap
	}
	saga.DeliveryID = e.AggregateID
	saga.DeliveryVersion = e.AggregateVersion
	change, err := r.fulfillment.HandleDelivery(ctx, &o, &saga, e, time.Now())
	if err != nil {
		return fmt.Errorf("apply delivery event: %w", err)
	}
	var confirmation []byte
	if e.Payload.Confirmation != nil {
		confirmation, err = json.Marshal(e.Payload.Confirmation)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_deliveries(order_id,delivery_id,version,payment_confirmation) VALUES($1,$2,$3,$4) ON CONFLICT(order_id) DO UPDATE SET version=EXCLUDED.version,payment_confirmation=COALESCE(EXCLUDED.payment_confirmation,order_deliveries.payment_confirmation)`, o.Snapshot().ID, e.AggregateID, e.AggregateVersion, confirmation)
	if err != nil {
		return err
	}
	if err = persistFulfillment(ctx, tx, o, saga, change); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
