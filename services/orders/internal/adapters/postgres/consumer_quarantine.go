package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	events "example.com/food/contracts/delivery/v1"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsumerInbox is local to this service. A conservative service-wide fence also
// covers dependencies between order events and commands on different topics.
// The existing Inbox remains the authority for business idempotency/versioning.
type ConsumerInbox struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	retry  ConsumerRetry
}

func NewConsumerInbox(pool *pgxpool.Pool, logger *slog.Logger) *ConsumerInbox {
	return &ConsumerInbox{pool: pool, logger: logger, retry: ConsumerRetry{Attempts: 5, Timeout: 5 * time.Second, Base: time.Second, Max: 8 * time.Second}}
}

func (r *ConsumerInbox) lock(ctx context.Context) (*pgxpool.Conn, func(), error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended(current_database()||':'||current_schema()||':consumer-quarantine',0))`)
	if err != nil { // A cancelled lock request may have reached PostgreSQL; discard the session.
		raw := conn.Hijack()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = raw.Close(cleanup)
		return nil, nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended(current_database()||':'||current_schema()||':consumer-quarantine',0))`)
		if e != nil {
			_ = conn.Hijack().Close(cleanup)
		} else {
			conn.Release()
		}
	}
	return conn, release, nil
}
func consumerError(err error) string {
	switch {
	case errors.Is(err, events.ErrUnsupported):
		return "unsupported_contract"
	case errors.Is(err, events.ErrInvalid):
		return "invalid_contract"
	case errors.Is(err, application.ErrEventGap):
		return "event_gap"
	case errors.Is(err, application.ErrEventConflict):
		return "event_conflict"
	case errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrSagaTransition) || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrPaymentMismatch):
		return "business_invariant"
	default:
		return "transient_or_internal"
	}
}
func messageIdentity(m application.Message) (string, string) {
	var envelope struct {
		EventID       string `json:"event_id"`
		MessageID     string `json:"message_id"`
		CorrelationID string `json:"correlation_id"`
	}
	_ = json.Unmarshal(m.Body, &envelope)
	if envelope.EventID == "" {
		envelope.EventID = envelope.MessageID
	}
	// Untrusted metadata is diagnostic only, bounded and never used for authorization.
	if len(envelope.EventID) > 128 {
		envelope.EventID = ""
	}
	if len(envelope.CorrelationID) > 128 {
		envelope.CorrelationID = ""
	}
	return envelope.EventID, envelope.CorrelationID
}

// Process returns success only after business effects or durable parking. Kafka
// commit happens in bootstrap afterwards and is never retried as a business error.
func (r *ConsumerInbox) Process(ctx context.Context, m application.Message, handle func(context.Context, application.Message) error) error {
	op, cancel := context.WithTimeout(ctx, time.Duration(r.retry.Attempts)*(r.retry.Timeout+r.retry.Max)+2*r.retry.Timeout)
	defer cancel()
	conn, release, err := r.lock(op)
	if err != nil {
		return err
	}
	defer release()

	var parked bool
	if err = conn.QueryRow(op, `SELECT EXISTS(SELECT 1 FROM consumer_quarantine WHERE topic=$1 AND partition=$2 AND offset_value=$3)`, m.Topic, m.Partition, m.Offset).Scan(&parked); err != nil {
		return err
	}
	// Offset commit can fail after parking or operator replay. The original bytes
	// must not overwrite an audited recovery or run a second time after resolution.
	if parked {
		return nil
	}
	var blocked bool
	if err = conn.QueryRow(op, `SELECT EXISTS(SELECT 1 FROM consumer_quarantine WHERE resolved_at IS NULL)`).Scan(&blocked); err != nil {
		return err
	}
	reason := "blocked_by_quarantine"
	attempts := 0
	eventID, correlationID := messageIdentity(m)
	log := r.logger.With("event_id", eventID, "correlation_id", correlationID, "topic", m.Topic, "partition", m.Partition, "offset", m.Offset)
	if !blocked {
		for attempts < r.retry.Attempts {
			attempts++
			attempt, cancel := context.WithTimeout(op, r.retry.Timeout)
			err = handle(attempt, m)
			cancel()
			if err == nil {
				log.Info("consumer message applied", "error_type", "none")
				return nil
			}
			reason = consumerError(err)
			if reason != "transient_or_internal" {
				break
			}
			log.Warn("consumer retry", "error_type", reason, "attempt", attempts)
			if attempts < r.retry.Attempts {
				maximum := min(r.retry.Base*time.Duration(1<<(attempts-1)), r.retry.Max)
				delay := maximum/2 + time.Duration(rand.Int64N(int64(maximum/2)+1))
				timer := time.NewTimer(delay)
				select {
				case <-op.Done():
					timer.Stop()
					return op.Err()
				case <-timer.C:
				}
			}
		}
		if reason == "transient_or_internal" {
			reason = "retries_exhausted"
		}
	}
	if op.Err() != nil {
		return op.Err()
	}
	key, body := m.Key, m.Body
	if key == nil {
		key = []byte{}
	}
	if body == nil {
		body = []byte{}
	}
	_, err = conn.Exec(op, `INSERT INTO consumer_quarantine(topic,partition,offset_value,message_key,body,reason,attempts,event_id,correlation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(topic,partition,offset_value) DO NOTHING`, m.Topic, m.Partition, m.Offset, key, body, reason, attempts, eventID, correlationID)
	if err != nil {
		return fmt.Errorf("persist consumer quarantine: %w", err)
	}
	log.Error("consumer message parked; business processing fenced", "error_type", reason, "attempts", attempts)
	return nil
}

// Replay is an operational command requiring database access. It never commits a
// Kafka offset. Inbox handles a crash between business commit and resolution.
// Recovery bytes are audited separately; the original record is never rewritten.
func (r *ConsumerInbox) Replay(ctx context.Context, id int64, operator, note string, body, key []byte, handle func(context.Context, application.Message) error) (result error) {
	if id <= 0 || operator == "" || note == "" || len(operator) > 200 || len(note) > 2000 {
		return errors.New("replay requires a failure ID, operator and resolution note")
	}
	conn, release, err := r.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	var m application.Message
	err = conn.QueryRow(ctx, `SELECT topic,partition,offset_value,message_key,body FROM consumer_quarantine WHERE id=$1 AND resolved_at IS NULL`, id).Scan(&m.Topic, &m.Partition, &m.Offset, &m.Key, &m.Body)
	if err != nil {
		return err
	}
	if body != nil {
		m.Body = body
	}
	if key != nil {
		m.Key = key
	}

	eventID, correlationID := messageIdentity(m)
	defer func() {
		fields := []any{"failure_id", id, "event_id", eventID, "correlation_id", correlationID, "topic", m.Topic, "partition", m.Partition, "offset", m.Offset, "error_type", "none"}
		if result != nil {
			fields[len(fields)-1] = consumerError(result)
			r.logger.Error("consumer replay failed; fence retained", fields...)
		} else {
			r.logger.Info("consumer replay committed", fields...)
		}
	}()
	var audit int64
	err = conn.QueryRow(ctx, `INSERT INTO consumer_replay_audit(failure_id,operator_name,note,recovery_body,recovery_key)VALUES($1,$2,$3,$4,$5)RETURNING id`, id, operator, note, m.Body, m.Key).Scan(&audit)
	if err != nil {
		return err
	}
	if err = handle(ctx, m); err != nil {
		return fmt.Errorf("replay rejected: %w", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `UPDATE consumer_quarantine SET resolved_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE consumer_replay_audit SET succeeded=true WHERE id=$1`, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *ConsumerInbox) Ready(ctx context.Context) error {
	var blocked bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM consumer_quarantine WHERE resolved_at IS NULL)`).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return errors.New("consumer business processing fenced by quarantine")
	}
	return nil
}

// ConsumerRetry is validated by bootstrap before workers start.
type ConsumerRetry struct {
	Attempts           int
	Timeout, Base, Max time.Duration
}

func (r ConsumerInbox) WithRetry(policy ConsumerRetry) *ConsumerInbox { r.retry = policy; return &r }

func (r *ConsumerInbox) LogCommitFailure(m application.Message) {
	eventID, correlationID := messageIdentity(m)
	r.logger.Error("consumer offset commit failed; record will replay", "topic", m.Topic, "partition", m.Partition, "offset", m.Offset, "event_id", eventID, "correlation_id", correlationID, "error_type", "offset_commit")
}
