package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
	"github.com/jackc/pgx/v5"
)

func insertIntent(ctx context.Context, tx pgx.Tx, n domain.Notification, fulfillment string) error {
	data, err := json.Marshal(n.Data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications(event_id,order_id,aggregate_version,customer_id,kind,status,correlation_id,fulfillment_type,recipient_type,recipient_id,title,body,data,expires_at,offer_id) VALUES($1,$2,$3,NULLIF($4,0),$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13,$14,NULLIF($15,0)) ON CONFLICT(event_id,recipient_type,recipient_id) DO NOTHING`, n.EventID, n.OrderID, n.Version, n.CustomerID, n.EventType, n.Status, n.CorrelationID, fulfillment, n.Recipient.Type, n.Recipient.ID, n.Title, n.Body, data, n.ExpiresAt, n.OfferID)
	return err
}

// ProcessBusiness commits canonical Inbox identity, relevance facts and intents together.
func (r *Inbox) ProcessBusiness(ctx context.Context, e application.BusinessEvent) error {
	eventID, stream, aggregate, version, body, intents, offer := e.ID, e.Stream, e.AggregateID, e.Version, e.Canonical, e.Intents, e.Offer
	hash := sha256.Sum256(body)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockEventIdentity(ctx, tx, eventID, "processed_events"); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO push_events(event_id,stream,aggregate_id,aggregate_version,fingerprint) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, eventID, stream, aggregate, version, hash[:])
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM push_events WHERE event_id=$1 AND stream=$2 AND aggregate_id=$3 AND aggregate_version=$4`, eventID, stream, aggregate, version).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(stored, hash[:]) {
			return application.ErrEventConflict
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if offer != nil {
		p := offer
		terminal := p.Terminal
		_, err = tx.Exec(ctx, `INSERT INTO offer_states(offer_id,terminal,expires_at) VALUES($1,$2,$3) ON CONFLICT(offer_id) DO UPDATE SET terminal=offer_states.terminal OR EXCLUDED.terminal`, p.ID, terminal, p.ExpiresAt)
		if err != nil {
			return err
		}
		if terminal {
			if err = cancelOfferIntents(ctx, tx, p.ID); err != nil {
				return err
			}
		}
		if !terminal {
			var closed bool
			if err = tx.QueryRow(ctx, `SELECT terminal FROM offer_states WHERE offer_id=$1`, p.ID).Scan(&closed); err != nil {
				return err
			}
			if closed {
				intents = nil
			}
		}
	}
	for _, n := range intents {
		if err = insertIntent(ctx, tx, n, ""); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func cancelOfferIntents(ctx context.Context, tx pgx.Tx, offer int64) error {
	// Claim, cancellation and completion all lock the parent before its deliveries.
	rows, err := tx.Query(ctx, `SELECT id FROM notifications WHERE offer_id=$1 AND kind='CourierOfferCreated' AND status IN ('PENDING','PROCESSING') FOR UPDATE`, offer)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = cancelIntent(ctx, tx, id, "offer_closed"); err != nil {
			return err
		}
	}
	return nil
}

func cancelOrderIntents(ctx context.Context, tx pgx.Tx, order, version int64) error {
	rows, err := tx.Query(ctx, `SELECT id FROM notifications WHERE order_id=$1 AND aggregate_version<$2 AND kind IN ('OrderCreated','OrderAccepted','OrderReadyForPickup','OrderOnTheWay') AND status IN ('PENDING','PROCESSING') ORDER BY id FOR UPDATE`, order, version)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = cancelIntent(ctx, tx, id, "order_terminal"); err != nil {
			return err
		}
	}
	return nil
}
func cancelIntent(ctx context.Context, tx pgx.Tx, id int64, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE push_deliveries SET status='CANCELLED',claim_token=NULL,lease_until=NULL,last_code=$2,updated_at=clock_timestamp() WHERE notification_id=$1 AND status='PENDING'`, id, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE notifications SET status=CASE WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1 AND status='PROCESSING') THEN 'PROCESSING' WHEN EXISTS(SELECT FROM push_deliveries WHERE notification_id=$1 AND status='SENT') THEN 'SENT' ELSE 'CANCELLED' END,reason=$2,updated_at=clock_timestamp() WHERE id=$1`, id, reason)
	return err
}

// The historical Orders Inbox keeps its original canonical hashes. Coordinate
// UUID ownership across it and the added streams without rewriting either audit.
func lockEventIdentity(ctx context.Context, tx pgx.Tx, id, other string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_database()||':'||current_schema()||':notification-event:'||$1::text,0))`, id); err != nil {
		return err
	}
	query := `SELECT EXISTS(SELECT FROM processed_events WHERE event_id=$1)`
	if other == "push_events" {
		query = `SELECT EXISTS(SELECT FROM push_events WHERE event_id=$1)`
	}
	var conflict bool
	if err := tx.QueryRow(ctx, query, id).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return application.ErrEventConflict
	}
	return nil
}
