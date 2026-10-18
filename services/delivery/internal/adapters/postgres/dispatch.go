package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

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
	var reason string
	err = tx.QueryRow(ctx, `SELECT id,dispatch_attempts,dispatch_reason FROM deliveries WHERE status='PENDING_ASSIGNMENT' AND NOT preparation_started AND NOT dispatch_paused
 AND NOT EXISTS(SELECT FROM courier_offers o WHERE o.delivery_id=deliveries.id AND o.status='PENDING')
 AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=deliveries.order_id AND s.dispatch_blocked)
 AND NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL)
 AND (dispatch_attempts<$1 OR (dispatch_reason='searching' AND dispatch_lease_until<=clock_timestamp()) OR dispatch_reason IN ('offer_declined','offer_expired','offer_cancelled')) AND dispatch_next_at<=clock_timestamp()
 AND (dispatch_lease_until IS NULL OR dispatch_lease_until<=clock_timestamp())
 ORDER BY dispatch_next_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, cfg.MaxAttempts).Scan(&id, &attempts, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DispatchClaim{}, false, nil
	}
	if err != nil {
		return application.DispatchClaim{}, false, err
	}
	// Close exhausted budgets after a lost lease or a terminal offer decision.
	// Neither a courier response nor a restart extends the retry budget.
	if attempts >= cfg.MaxAttempts {
		if reason == "searching" {
			reason = "lease_expired"
		}
		if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_paused=true,dispatch_token=NULL,dispatch_lease_until=NULL,dispatch_reason=$2 WHERE id=$1`, id, "attempts_exhausted:"+reason); err != nil {
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

func (r *Repository) OfferDispatch(ctx context.Context, claim application.DispatchClaim, candidates []application.Candidate, cooldown time.Duration) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	// Cooperate with the existing consumer fence without holding it across Redis.
	if err = assignmentFence(ctx, tx); errors.Is(err, domain.ErrConflict) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	d, err := loadDelivery(ctx, tx, claim.Delivery.ID)
	if err != nil {
		return false, err
	}
	if d.Status != domain.PendingAssignment || d.PreparationStarted {
		return false, nil
	}
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(dispatch_token=$2::uuid AND dispatch_lease_until>clock_timestamp() AND dispatch_deadline>clock_timestamp() AND NOT EXISTS(SELECT FROM courier_offers o WHERE o.delivery_id=deliveries.id AND o.status='PENDING') AND NOT EXISTS(SELECT FROM order_streams s WHERE s.order_id=deliveries.order_id AND s.dispatch_blocked),false) FROM deliveries WHERE id=$1`, d.ID, claim.Token).Scan(&owned); err != nil {
		return false, err
	}
	if !owned {
		return false, nil
	}
	for _, candidate := range candidates {
		var id int64
		err = tx.QueryRow(ctx, `SELECT c.id FROM couriers c WHERE c.id=$1 AND c.status='AVAILABLE' AND $4::timestamptz>clock_timestamp()
 AND NOT EXISTS(SELECT FROM courier_offers o WHERE o.courier_id=c.id AND o.status='PENDING')
 AND NOT EXISTS(SELECT FROM dispatch_declines WHERE delivery_id=$2 AND courier_id=c.id AND declined_at>clock_timestamp()-$3*interval '1 millisecond')
 FOR UPDATE OF c SKIP LOCKED`, candidate.CourierID, d.ID, cooldown.Milliseconds(), candidate.ExpiresAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		var reserved bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM courier_offers WHERE courier_id=$1 AND status='PENDING')`, id).Scan(&reserved); err != nil {
			return false, err
		}
		if reserved {
			continue
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp(),dispatch_lease_until>clock_timestamp() FROM deliveries WHERE id=$1`, d.ID).Scan(&now, &owned); err != nil {
			return false, err
		}
		if !owned {
			return false, nil
		}
		if !candidate.ExpiresAt.After(now) {
			continue
		}
		offer, err := domain.NewCourierOffer(d.ID, id, now, *d.DispatchDeadline)
		if err != nil {
			return false, nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO courier_offers(delivery_id,courier_id,status,expires_at,created_at) VALUES($1,$2,$3,$4,$5)`, offer.DeliveryID, offer.CourierID, offer.Status, offer.ExpiresAt, offer.CreatedAt); err != nil {
			return false, conflict(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE deliveries SET dispatch_token=NULL,dispatch_lease_until=NULL,dispatch_reason='awaiting_courier_acceptance' WHERE id=$1`, d.ID); err != nil {
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
 WHERE id=$1 AND status='PENDING_ASSIGNMENT' AND NOT preparation_started AND NOT EXISTS(SELECT FROM courier_offers o WHERE o.delivery_id=deliveries.id AND o.status='PENDING') AND dispatch_deadline>clock_timestamp() AND latitude BETWEEN -85.05112878 AND 85.05112878
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
