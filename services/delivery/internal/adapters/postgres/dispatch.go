package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	events "github.com/Desu-php/food/contracts/delivery/v1"
	ids "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func (r *Repository) CourierAvailable(ctx context.Context, id int64) error {
	var status string
	err := r.pool.QueryRow(ctx, `SELECT status FROM couriers WHERE id=$1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != domain.Available {
		return domain.ErrConflict
	}
	return nil
}

// The lease is committed before Redis is called. Expired owners are fenced by token.
func (r *Repository) ClaimDispatch(ctx context.Context, cfg application.DispatchConfig) (application.DispatchClaim, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return application.DispatchClaim{}, false, err
	}
	defer rollback(tx)
	var id int64
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id,dispatch_attempts FROM deliveries WHERE status='PENDING_ASSIGNMENT' AND NOT preparation_started AND NOT dispatch_paused
 AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=deliveries.order_id AND s.dispatch_blocked)
 AND NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL)
 AND (dispatch_attempts<$1 OR (dispatch_reason='searching' AND dispatch_lease_until<=clock_timestamp())) AND dispatch_next_at<=clock_timestamp()
 AND (dispatch_lease_until IS NULL OR dispatch_lease_until<=clock_timestamp())
 ORDER BY dispatch_next_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, cfg.MaxAttempts).Scan(&id, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DispatchClaim{}, false, nil
	}
	if err != nil {
		return application.DispatchClaim{}, false, err
	}
	// If the last owner died, close its exhausted budget instead of leaving
	// "searching" forever or retrying beyond the configured bound.
	if attempts >= cfg.MaxAttempts {
		if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_paused=true,dispatch_token=NULL,dispatch_lease_until=NULL,dispatch_reason='attempts_exhausted:lease_expired' WHERE id=$1`, id); err != nil {
			return application.DispatchClaim{}, false, err
		}
		return application.DispatchClaim{}, false, tx.Commit(ctx)
	}
	token := ids.NewID()
	if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_token=$2,dispatch_lease_until=clock_timestamp()+$3*interval '1 millisecond',dispatch_attempts=dispatch_attempts+1,dispatch_reason='searching' WHERE id=$1`, id, token, cfg.Lease.Milliseconds()); err != nil {
		return application.DispatchClaim{}, false, err
	}
	d, err := loadDelivery(ctx, tx, id)
	if err != nil {
		return application.DispatchClaim{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return application.DispatchClaim{}, false, err
	}
	return application.DispatchClaim{Delivery: d, Token: token}, true, nil
}

func (r *Repository) AssignDispatch(ctx context.Context, claim application.DispatchClaim, candidates []application.Candidate, cooldown time.Duration) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	// Cooperate with the existing consumer fence without holding it across Redis.
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(hashtextextended(current_database()||':'||current_schema()||':consumer-quarantine',0))`).Scan(&allowed); err != nil {
		return false, err
	}
	if !allowed {
		return false, nil
	}
	if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL)`).Scan(&allowed); err != nil {
		return false, err
	}
	if !allowed {
		return false, nil
	}
	d, err := loadDelivery(ctx, tx, claim.Delivery.ID)
	if err != nil {
		return false, err
	}
	if d.Status != domain.PendingAssignment || d.PreparationStarted {
		return false, nil
	}
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(dispatch_token=$2::uuid AND dispatch_lease_until>clock_timestamp() AND dispatch_deadline>clock_timestamp() AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=deliveries.order_id AND s.dispatch_blocked),false) FROM deliveries WHERE id=$1`, d.ID, claim.Token).Scan(&owned); err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	for _, candidate := range candidates {
		var id int64
		err = tx.QueryRow(ctx, `SELECT c.id FROM couriers c WHERE c.id=$1 AND c.status='AVAILABLE' AND $4::timestamptz>clock_timestamp()
 AND NOT EXISTS(SELECT FROM dispatch_declines WHERE delivery_id=$2 AND courier_id=c.id AND declined_at>clock_timestamp()-$3*interval '1 millisecond')
 FOR UPDATE OF c SKIP LOCKED`, candidate.CourierID, d.ID, cooldown.Milliseconds(), candidate.ExpiresAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		old := d
		if err = d.Assign(id, time.Now()); err != nil {
			return false, err
		}
		if _, err = writeDeliveryChange(ctx, tx, old, d, events.CourierAssigned); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_token=NULL,dispatch_lease_until=NULL,dispatch_reason='assigned' WHERE id=$1`, d.ID); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	return false, nil
}
func (r *Repository) FinishDispatch(ctx context.Context, claim application.DispatchClaim, cfg application.DispatchConfig, reason string, stop bool) error {
	_, err := r.pool.Exec(ctx, `UPDATE deliveries SET dispatch_token=NULL,dispatch_lease_until=NULL,dispatch_reason=CASE WHEN NOT $4 AND dispatch_attempts >= $5 THEN 'attempts_exhausted:' || $3 ELSE $3 END,
 dispatch_paused=($4 OR dispatch_attempts >= $5),dispatch_next_at=clock_timestamp()+$6*interval '1 millisecond'
 WHERE id=$1 AND dispatch_token=$2 AND status='PENDING_ASSIGNMENT'`, claim.Delivery.ID, claim.Token, reason, stop, cfg.MaxAttempts, cfg.RetryInterval.Milliseconds())
	return err
}

// Retry cannot extend a Saga-owned deadline or restart automatic dispatch after cooking.
func (r *Repository) RetryDispatch(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `UPDATE deliveries SET dispatch_attempts=0,dispatch_paused=false,dispatch_next_at=now(),dispatch_reason='',dispatch_token=NULL,dispatch_lease_until=NULL
 WHERE id=$1 AND status='PENDING_ASSIGNMENT' AND NOT preparation_started AND dispatch_deadline>clock_timestamp() AND latitude BETWEEN -85.05112878 AND 85.05112878
 AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=deliveries.order_id AND s.dispatch_blocked)`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM deliveries WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
		return domain.ErrConflict
	}
	return nil
}
