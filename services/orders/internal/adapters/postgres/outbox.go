package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/orders/internal/application"
)

func insertEvent(ctx context.Context, tx pgx.Tx, e events.Event) error {
	if err := e.Validate(); err != nil {
		return fmt.Errorf("validate outbox event: %w", err)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode outbox event: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,aggregate_version,event_type,correlation_id,body) VALUES($1,$2,$3,$4,$5,$6)`, e.EventID, e.AggregateID, e.AggregateVersion, e.EventType, e.CorrelationID, body)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

type Outbox struct{ pool *pgxpool.Pool }

func NewOutbox(pool *pgxpool.Pool) *Outbox { return &Outbox{pool: pool} }

var _ application.OutboxRepository = (*Outbox)(nil)

func (r *Outbox) Claim(ctx context.Context, lease time.Duration) (application.ClaimedEvent, bool, error) {
	var e application.ClaimedEvent
	e.Token = events.NewID()
	// A single statement commits the claim before returning to the publisher.
	// Earlier DEAD events intentionally block later versions until an operator requeues them.
	err := r.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT e.event_id,e.status='PROCESSING' AS recovered FROM outbox_events e
 WHERE ((e.status='PENDING' AND e.available_at<=now()) OR (e.status='PROCESSING' AND e.lease_until<=now()))
 AND NOT EXISTS (SELECT 1 FROM outbox_events prev WHERE prev.stream=e.stream AND prev.aggregate_id=e.aggregate_id AND prev.aggregate_version<e.aggregate_version AND prev.status<>'PUBLISHED')
 ORDER BY e.created_at,e.aggregate_id,e.aggregate_version FOR UPDATE OF e SKIP LOCKED LIMIT 1
 ) UPDATE outbox_events e SET status='PROCESSING',claim_token=$1,lease_until=now()+$2*interval '1 millisecond',attempts=e.attempts+1
 FROM candidate c WHERE e.event_id=c.event_id
 RETURNING e.event_id::text,e.aggregate_id,e.aggregate_version,e.correlation_id,e.body,e.attempts,c.recovered,e.stream`, e.Token, lease.Milliseconds()).Scan(&e.ID, &e.AggregateID, &e.Version, &e.CorrelationID, &e.Body, &e.Attempts, &e.Recovered, &e.Stream)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, fmt.Errorf("claim outbox event: %w", err)
	}
	return e, true, nil
}

func (r *Outbox) Published(ctx context.Context, e application.ClaimedEvent) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status='PUBLISHED',published_at=now(),claim_token=NULL,lease_until=NULL,last_error=NULL WHERE event_id=$1 AND claim_token=$2 AND status='PROCESSING' AND lease_until>now()`, e.ID, e.Token)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrLeaseLost
	}
	return nil
}

func (r *Outbox) Failed(ctx context.Context, e application.ClaimedEvent, delay time.Duration, dead bool) error {
	status := "PENDING"
	if dead {
		status = "DEAD"
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status=$3,available_at=now()+$4*interval '1 millisecond',claim_token=NULL,lease_until=NULL,last_error='kafka_publish_failed' WHERE event_id=$1 AND claim_token=$2 AND status='PROCESSING' AND lease_until>now()`, e.ID, e.Token, status, delay.Milliseconds())
	if err != nil {
		return fmt.Errorf("reschedule outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrLeaseLost
	}
	return nil
}

func (r *Outbox) Requeue(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status='PENDING',attempts=0,available_at=now(),last_error=NULL WHERE event_id=$1 AND status='DEAD'`, id)
	if err != nil {
		return fmt.Errorf("requeue outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("DEAD outbox event not found")
	}
	return nil
}

func (r *Outbox) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	// Bounded batches limit locks/WAL; unpublished records are never deleted.
	tag, err := r.pool.Exec(ctx, `WITH old AS (SELECT event_id FROM outbox_events WHERE status='PUBLISHED' AND published_at<now()-$1*interval '1 millisecond' ORDER BY published_at FOR UPDATE SKIP LOCKED LIMIT 1000) DELETE FROM outbox_events e USING old WHERE e.event_id=old.event_id`, retention.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("prune outbox: %w", err)
	}
	return tag.RowsAffected(), nil
}
