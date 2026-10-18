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

	commands "example.com/food/contracts/delivery/commands/v1"
	events "example.com/food/contracts/delivery/v1"
	"example.com/food/delivery/internal/application"
	"example.com/food/delivery/internal/domain"
)

func insertCommandResponse(ctx context.Context, tx pgx.Tx, d domain.Delivery, kind, cause, reason string) (string, error) {
	e := application.DeliveryEvent(ctx, d, kind)
	e.CausationID = cause
	e.Payload.Reason = reason
	if err := e.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,aggregate_version,event_type,correlation_id,body) VALUES($1,$2,$3,$4,$5,$6)`, e.EventID, e.AggregateID, e.AggregateVersion, e.EventType, e.CorrelationID, body)
	return e.EventID, err
}
func (r *Repository) ProcessCommand(ctx context.Context, c commands.Command) error {
	if err := c.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin command: %w", err)
	}
	defer rollback(tx)
	tag, err := tx.Exec(ctx, `INSERT INTO command_inbox(message_id,order_id,command_version,fingerprint) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.MessageID, c.AggregateID, c.AggregateVersion, hash[:])
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		var response *string
		err = tx.QueryRow(ctx, `SELECT fingerprint,response_event_id::text FROM command_inbox WHERE message_id=$1 AND order_id=$2 AND command_version=$3`, c.MessageID, c.AggregateID, c.AggregateVersion).Scan(&stored, &response)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(stored, hash[:]) {
			return application.ErrEventConflict
		}
		if err != nil {
			return err
		}
		if response != nil {
			if _, err = tx.Exec(ctx, `UPDATE outbox_events SET status='PENDING',published_at=NULL,available_at=now(),attempts=0,last_error=NULL WHERE event_id=$1 AND status IN ('PUBLISHED','DEAD')`, *response); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO delivery_requests(order_id) VALUES($1) ON CONFLICT DO NOTHING`, c.AggregateID); err != nil {
		return err
	}
	var commandVersion int64
	var cancelled bool
	if err = tx.QueryRow(ctx, `SELECT command_version,cancelled FROM delivery_requests WHERE order_id=$1 FOR UPDATE`, c.AggregateID).Scan(&commandVersion, &cancelled); err != nil {
		return err
	}
	// Cancellation tombstones fence late CreateDelivery, including a different message ID.
	if c.AggregateVersion <= commandVersion {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_streams(order_id) VALUES($1) ON CONFLICT DO NOTHING`, c.AggregateID); err != nil {
		return err
	}
	var preparation, ready bool
	if err = tx.QueryRow(ctx, `SELECT preparation_started,ready FROM order_streams WHERE order_id=$1 FOR UPDATE`, c.AggregateID).Scan(&preparation, &ready); err != nil {
		return err
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM deliveries WHERE order_id=$1`, c.AggregateID).Scan(&id)
	var current *domain.Delivery
	if err == nil {
		loaded, loadErr := loadDelivery(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}
		current = &loaded
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	change, err := application.ApplyDeliveryCommand(c, current, preparation, ready, cancelled, time.Now())
	if err != nil {
		return fmt.Errorf("apply delivery command: %w", err)
	}
	d := change.Delivery
	var latitude, longitude *float64
	if d.RestaurantLocation != nil {
		latitude = &d.RestaurantLocation.Latitude
		longitude = &d.RestaurantLocation.Longitude
	}
	if current == nil {
		err = tx.QueryRow(ctx, `INSERT INTO deliveries(order_id,status,amount_to_collect_minor,currency,version,order_version,preparation_started,ready,created_at,updated_at,delivery_address,latitude,longitude,dispatch_deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`, d.OrderID, d.Status, d.AmountToCollectMinor, d.Currency, d.Version, d.OrderVersion, d.PreparationStarted, d.Ready, d.CreatedAt, d.UpdatedAt, d.DeliveryAddress, latitude, longitude, d.DispatchDeadline).Scan(&d.ID)
		if err != nil {
			return err
		}
	} else if err = saveDelivery(ctx, tx, d); err != nil {
		return err
	}
	if c.CommandType == commands.CreateDelivery && current != nil && d.Status == domain.PendingAssignment && c.Payload.DispatchDeadline != nil {
		if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_attempts=0,dispatch_paused=false,dispatch_next_at=now(),dispatch_reason='',dispatch_token=NULL,dispatch_lease_until=NULL WHERE id=$1`, d.ID); err != nil {
			return err
		}
	}
	if change.ReleaseCourierID > 0 {
		if _, err = tx.Exec(ctx, `UPDATE couriers SET status='AVAILABLE',updated_at=$2 WHERE id=$1`, change.ReleaseCourierID, d.UpdatedAt); err != nil {
			return err
		}
	}
	if change.Created != nil {
		created := *change.Created
		created.ID = d.ID
		if _, err = insertCommandResponse(ctx, tx, created, events.DeliveryCreated, c.MessageID, ""); err != nil {
			return err
		}
	}
	responseID, err := insertCommandResponse(ctx, tx, d, change.EventType, c.MessageID, change.Reason)
	if err != nil {
		return err
	}
	cancelled = change.Cancelled
	if _, err = tx.Exec(ctx, `UPDATE command_inbox SET response_event_id=$2 WHERE message_id=$1`, c.MessageID, responseID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE delivery_requests SET command_version=$2,cancelled=$3 WHERE order_id=$1`, c.AggregateID, c.AggregateVersion, cancelled); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
