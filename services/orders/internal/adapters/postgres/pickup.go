package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	events "example.com/food/contracts/orders/v1"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Orders) IssuePickupCode(ctx context.Context, customer, id int64, hash []byte, expires time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	o, err := load(ctx, tx, customer, id, true)
	if err != nil {
		return err
	}
	s := o.Snapshot()
	if s.FulfillmentType != domain.Pickup || s.Status != domain.ReadyForPickup {
		return domain.ErrInvalidTransition
	}
	// Rotation invalidates the old token. It never changes order versions or events.
	_, err = tx.Exec(ctx, `INSERT INTO pickup_codes(order_id,code_hash,expires_at) VALUES($1,$2,$3)
 ON CONFLICT(order_id) DO UPDATE SET code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at,attempts=0,blocked_until=NULL,created_at=now()`, id, hash, expires)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Orders) CompletePickup(ctx context.Context, user, restaurant, id int64, c application.PickupCompletion) (domain.Order, error) {
	return r.pickup(ctx, user, restaurant, id, c, true)
}
func (r *Orders) VerifyPickup(ctx context.Context, restaurant, id int64, code string) (domain.Order, error) {
	return r.pickup(ctx, 0, restaurant, id, application.PickupCompletion{Code: code}, false)
}
func (r *Orders) pickup(ctx context.Context, user, restaurant, id int64, c application.PickupCompletion, complete bool) (domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, err
	}
	defer rollback(tx)
	var customer int64
	err = tx.QueryRow(ctx, `SELECT customer_id FROM orders WHERE id=$1 AND restaurant_id=$2 FOR UPDATE`, id, restaurant).Scan(&customer)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Order{}, err
	}
	o, err := load(ctx, tx, customer, id, false)
	if err != nil {
		return domain.Order{}, err
	}
	s := o.Snapshot()
	if s.FulfillmentType != domain.Pickup || s.Status != domain.ReadyForPickup && !(complete && s.Status == domain.PickedUpByCustomer) {
		return domain.Order{}, domain.ErrInvalidTransition
	}
	var stored []byte
	var expires time.Time
	var attempts int
	var blocked *time.Time
	err = tx.QueryRow(ctx, `SELECT code_hash,expires_at,attempts,blocked_until FROM pickup_codes WHERE order_id=$1 FOR UPDATE`, id).Scan(&stored, &expires, &attempts, &blocked)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, application.ErrPickupCode
	}
	if err != nil {
		return domain.Order{}, err
	}
	// Match PostgreSQL precision so the initial result equals a replay after restart.
	now := time.Now().UTC().Truncate(time.Microsecond)
	if blocked != nil && now.Before(*blocked) {
		return domain.Order{}, application.ErrPickupRateLimited
	}
	hash := sha256.Sum256([]byte(c.Code))
	if subtle.ConstantTimeCompare(stored, hash[:]) != 1 || s.Status != domain.PickedUpByCustomer && !now.Before(expires) {
		if blocked != nil {
			attempts = 0
		}
		attempts++
		_, err = tx.Exec(ctx, `UPDATE pickup_codes SET attempts=$2,blocked_until=CASE WHEN $2>=5 THEN $3::timestamptz ELSE NULL END WHERE order_id=$1`, id, attempts, now.Add(15*time.Minute))
		if err != nil {
			return domain.Order{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Order{}, err
		}
		return domain.Order{}, application.ErrPickupCode
	}
	if !complete {
		if err = tx.Commit(ctx); err != nil {
			return domain.Order{}, err
		}
		return o, nil
	}
	if s.Status == domain.PickedUpByCustomer {
		var by int64
		var receipt string
		if err = tx.QueryRow(ctx, `SELECT confirmed_by,receipt_reference FROM pickup_payment_confirmations WHERE order_id=$1`, id).Scan(&by, &receipt); err != nil {
			return domain.Order{}, err
		}
		if by != user || receipt != c.ReceiptReference || !c.PaymentConfirmed {
			return domain.Order{}, application.ErrIdempotencyConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Order{}, err
		}
		return o, nil
	}
	if err = o.CompletePickup(c.PaymentConfirmed, now); err != nil {
		return domain.Order{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pickup_payment_confirmations(order_id,confirmed_by,amount_minor,currency,payment_method,confirmed_at,receipt_reference) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, user, s.SubtotalMinor, s.Currency, domain.POSAtPickup, now, c.ReceiptReference)
	if err != nil {
		return domain.Order{}, err
	}
	if err = saveOrder(ctx, tx, o, events.OrderPickedUpByCustomer); err != nil {
		return domain.Order{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Order{}, err
	}
	return o, nil
}
