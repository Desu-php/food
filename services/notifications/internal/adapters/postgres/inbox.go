package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Inbox struct{ pool *pgxpool.Pool }

func NewInbox(pool *pgxpool.Pool) *Inbox { return &Inbox{pool: pool} }

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (r *Inbox) Process(ctx context.Context, e events.Event, n domain.Notification) (bool, error) {
	// Canonical known fields tolerate JSON formatting and additive field changes.
	body, err := json.Marshal(e)
	if err != nil {
		return false, fmt.Errorf("encode fingerprint: %w", err)
	}
	hash := sha256.Sum256(body)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin inbox: %w", err)
	}
	defer rollback(tx)
	tag, err := tx.Exec(ctx, `INSERT INTO processed_events(event_id,order_id,aggregate_version,fingerprint) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, e.EventID, e.AggregateID, e.AggregateVersion, hash[:])
	if err != nil {
		return false, fmt.Errorf("insert inbox event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var stored []byte
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM processed_events WHERE event_id=$1 AND order_id=$2 AND aggregate_version=$3`, e.EventID, e.AggregateID, e.AggregateVersion).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(stored, hash[:]) {
			return false, application.ErrEventConflict
		}
		if err != nil {
			return false, fmt.Errorf("check duplicate: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate: %w", err)
		}
		return true, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications(event_id,order_id,aggregate_version,customer_id,kind,status,correlation_id,fulfillment_type) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))`, n.EventID, n.OrderID, n.Version, n.CustomerID, n.Kind, n.Status, n.CorrelationID, e.Payload.FulfillmentType)
	if err != nil {
		return false, fmt.Errorf("insert notification: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit inbox: %w", err)
	}
	return false, nil
}

func (r *Inbox) Quarantine(ctx context.Context, m application.Message, reason string, attempts int) error {
	key := m.Key
	if key == nil {
		key = []byte{}
	}
	body := m.Body
	if body == nil {
		body = []byte{}
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO failed_messages(topic,partition,offset_value,message_key,body,reason,attempts) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(topic,partition,offset_value) DO UPDATE SET reason=EXCLUDED.reason,attempts=failed_messages.attempts+EXCLUDED.attempts,status='QUARANTINED',resolved_at=NULL`, m.Topic, m.Partition, m.Offset, key, body, reason, attempts)
	if err != nil {
		return fmt.Errorf("quarantine message: %w", err)
	}
	return nil
}

func (r *Inbox) FailedMessage(ctx context.Context, id int64) (application.Message, error) {
	var m application.Message
	err := r.pool.QueryRow(ctx, `SELECT topic,partition,offset_value,message_key,body FROM failed_messages WHERE id=$1 AND status='QUARANTINED'`, id).Scan(&m.Topic, &m.Partition, &m.Offset, &m.Key, &m.Body)
	if err != nil {
		return m, fmt.Errorf("load failed message: %w", err)
	}
	return m, nil
}

func (r *Inbox) Resolve(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `UPDATE failed_messages SET status='RESOLVED',resolved_at=now() WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("resolve failed message: %w", err)
	}
	return nil
}
