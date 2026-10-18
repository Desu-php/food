package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"example.com/food/contracts/correlation"
	commands "example.com/food/contracts/delivery/commands/v1"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

const sagaColumns = `order_id,state,version,delivery_id,delivery_version,courier_id,command_id,command_type,command_sequence,attempts,deadline,next_attempt_at,reason,correlation_id,created_at,updated_at,cancel_reason,legacy_reconciliation,dispatch_deadline`

func loadOrderSaga(ctx context.Context, tx pgx.Tx, o domain.Order) (domain.Saga, error) {
	if o.Snapshot().FulfillmentType == domain.Pickup {
		return domain.Saga{}, nil
	}
	return loadSaga(ctx, tx, o.Snapshot().ID)
}

func loadSaga(ctx context.Context, tx pgx.Tx, id int64) (domain.Saga, error) {
	var s domain.Saga
	err := tx.QueryRow(ctx, `SELECT `+sagaColumns+` FROM order_sagas WHERE order_id=$1 FOR UPDATE`, id).Scan(&s.OrderID, &s.State, &s.Version, &s.DeliveryID, &s.DeliveryVersion, &s.CourierID, &s.CommandID, &s.CommandType, &s.CommandSequence, &s.Attempts, &s.Deadline, &s.NextAttempt, &s.Reason, &s.CorrelationID, &s.CreatedAt, &s.UpdatedAt, &s.CancelReason, &s.LegacyReconciliation, &s.DispatchDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, domain.ErrNotFound
	}
	if err != nil {
		return s, fmt.Errorf("load saga: %w", err)
	}
	return s, nil
}
func saveSaga(ctx context.Context, tx pgx.Tx, s domain.Saga) error {
	_, err := tx.Exec(ctx, `INSERT INTO order_sagas(`+sagaColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
 ON CONFLICT(order_id) DO UPDATE SET state=EXCLUDED.state,version=EXCLUDED.version,delivery_id=EXCLUDED.delivery_id,delivery_version=EXCLUDED.delivery_version,courier_id=EXCLUDED.courier_id,command_id=EXCLUDED.command_id,command_type=EXCLUDED.command_type,command_sequence=EXCLUDED.command_sequence,attempts=EXCLUDED.attempts,deadline=EXCLUDED.deadline,next_attempt_at=EXCLUDED.next_attempt_at,reason=EXCLUDED.reason,correlation_id=EXCLUDED.correlation_id,updated_at=EXCLUDED.updated_at,cancel_reason=EXCLUDED.cancel_reason,legacy_reconciliation=EXCLUDED.legacy_reconciliation,dispatch_deadline=EXCLUDED.dispatch_deadline`, s.OrderID, s.State, s.Version, s.DeliveryID, s.DeliveryVersion, s.CourierID, s.CommandID, s.CommandType, s.CommandSequence, s.Attempts, s.Deadline, s.NextAttempt, s.Reason, s.CorrelationID, s.CreatedAt, s.UpdatedAt, s.CancelReason, s.LegacyReconciliation, s.DispatchDeadline)
	if err != nil {
		return fmt.Errorf("save saga: %w", err)
	}
	return nil
}
func insertCommand(ctx context.Context, tx pgx.Tx, c commands.Command) error {
	if err := c.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,aggregate_version,event_type,correlation_id,body,stream) VALUES($1,$2,$3,$4,$5,$6,'delivery_commands')`, c.MessageID, c.AggregateID, c.AggregateVersion, c.CommandType, c.CorrelationID, body)
	if err != nil {
		return fmt.Errorf("persist saga command: %w", err)
	}
	return nil
}
func persistFulfillment(ctx context.Context, tx pgx.Tx, o domain.Order, s domain.Saga, c application.FulfillmentChange) error {
	if c.EventKind != "" {
		if err := saveOrder(ctx, tx, o, c.EventKind); err != nil {
			return err
		}
	}
	if o.Snapshot().FulfillmentType == domain.Delivery {
		if err := saveSaga(ctx, tx, s); err != nil {
			return err
		}
	} else if len(c.Commands) > 0 || c.ReplayCommandID != "" {
		return domain.ErrInvalidTransition
	}
	if c.ReplayCommandID != "" {
		// Never steal an active lease. Its worker will publish or release it for retry.
		_, err := tx.Exec(ctx, `UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0,last_error=NULL WHERE event_id=$1 AND stream='delivery_commands' AND status IN ('PUBLISHED','DEAD')`, c.ReplayCommandID)
		if err != nil {
			return fmt.Errorf("retry saga command: %w", err)
		}
	}
	for _, cmd := range c.Commands {
		if err := insertCommand(ctx, tx, cmd); err != nil {
			return err
		}
	}
	return nil
}
func (r *Orders) Saga(ctx context.Context, restaurant, id int64) (domain.Saga, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Saga{}, err
	}
	defer rollback(tx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM orders WHERE restaurant_id=$1 AND id=$2)`, restaurant, id).Scan(&exists); err != nil {
		return domain.Saga{}, err
	}
	if !exists {
		return domain.Saga{}, domain.ErrNotFound
	}
	s, err := loadSaga(ctx, tx, id)
	if err != nil {
		return s, err
	}
	return s, tx.Commit(ctx)
}
func (r *Orders) StepSaga(ctx context.Context, now time.Time) (domain.Saga, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Saga{}, false, err
	}
	defer rollback(tx)
	var id, customer int64
	err = tx.QueryRow(ctx, `SELECT o.id,o.customer_id FROM orders o JOIN order_sagas s ON s.order_id=o.id WHERE s.state NOT IN ('WAITING_RESTAURANT','COMPLETED','CANCELLED','NEEDS_ATTENTION') AND s.next_attempt_at<=$1 ORDER BY s.next_attempt_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT 1`, now).Scan(&id, &customer)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Saga{}, false, nil
	}
	if err != nil {
		return domain.Saga{}, false, fmt.Errorf("claim due saga: %w", err)
	}
	o, err := load(ctx, tx, customer, id, false)
	if err != nil {
		return domain.Saga{}, false, err
	}
	s, err := loadSaga(ctx, tx, id)
	if err != nil {
		return s, false, err
	}
	ctx = correlation.WithID(ctx, s.CorrelationID)
	change, err := r.fulfillment.Tick(ctx, &o, &s, now)
	if err != nil {
		return s, false, err
	}
	if err = persistFulfillment(ctx, tx, o, s, change); err != nil {
		return s, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return s, false, err
	}
	return s, true, nil
}
