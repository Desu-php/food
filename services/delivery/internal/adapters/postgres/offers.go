package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	events "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

const offerColumns = `id,delivery_id,courier_id,status,expires_at,created_at,responded_at`

func scanOffer(row pgx.Row) (domain.CourierOffer, error) {
	var o domain.CourierOffer
	err := row.Scan(&o.ID, &o.DeliveryID, &o.CourierID, &o.Status, &o.ExpiresAt, &o.CreatedAt, &o.RespondedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return o, err
}

// Only live reservations are listed; historical decisions remain in PostgreSQL.
func (r *Repository) Offers(ctx context.Context, courier int64) ([]domain.CourierOffer, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+offerColumns+` FROM courier_offers WHERE courier_id=$1 AND status='PENDING' AND expires_at>clock_timestamp() ORDER BY id`, courier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.CourierOffer{}
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

func saveOffer(ctx context.Context, tx pgx.Tx, o domain.CourierOffer) error {
	_, err := tx.Exec(ctx, `UPDATE courier_offers SET status=$2,responded_at=$3 WHERE id=$1`, o.ID, o.Status, o.RespondedAt)
	if err != nil {
		return err
	}
	return insertOfferEvent(ctx, tx, o)
}

func cancelOffers(ctx context.Context, tx pgx.Tx, delivery int64) error {
	rows, err := tx.Query(ctx, `UPDATE courier_offers SET status='CANCELLED',responded_at=clock_timestamp() WHERE delivery_id=$1 AND status='PENDING' RETURNING `+offerColumns, delivery)
	if err != nil {
		return err
	}
	var changed []domain.CourierOffer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			rows.Close()
			return err
		}
		changed = append(changed, o)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, o := range changed {
		if err = insertOfferEvent(ctx, tx, o); err != nil {
			return err
		}
	}
	return nil
}

// Delivery -> offer -> courier is the lock order for responses. All cancellation
// paths own the same delivery lock; no transaction waits for a human or Redis.
func (r *Repository) RespondOffer(ctx context.Context, courier, id int64, accept bool) (domain.CourierOffer, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.CourierOffer{}, err
	}
	defer rollback(tx)
	if accept {
		if err = assignmentFence(ctx, tx); err != nil {
			return domain.CourierOffer{}, err
		}
	}
	var delivery int64
	err = tx.QueryRow(ctx, `SELECT delivery_id FROM courier_offers WHERE id=$1 AND courier_id=$2`, id, courier).Scan(&delivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CourierOffer{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.CourierOffer{}, err
	}
	d, err := loadDelivery(ctx, tx, delivery)
	if err != nil {
		return domain.CourierOffer{}, err
	}
	o, err := scanOffer(tx.QueryRow(ctx, `SELECT `+offerColumns+` FROM courier_offers WHERE id=$1 AND courier_id=$2 FOR UPDATE`, id, courier))
	if err != nil {
		return o, err
	}
	// Exact decision retries return the original audit even after a later release.
	if (accept && o.Status == domain.OfferAccepted) || (!accept && o.Status == domain.OfferDeclined) {
		return o, tx.Commit(ctx)
	}
	if o.Status != domain.OfferPending {
		return o, domain.ErrConflict
	}
	// Lock the courier before reading the clock or checking availability. A SQL
	// wait must not turn a response arriving after TTL into an accepted offer.
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM couriers WHERE id=$1 FOR UPDATE`, courier).Scan(&status); err != nil {
		return o, err
	}
	var now time.Time
	var blocked bool
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp(),EXISTS(SELECT FROM order_streams WHERE order_id=$1 AND dispatch_blocked)`, d.OrderID).Scan(&now, &blocked); err != nil {
		return o, err
	}
	if o.Expire(now) {
		if err = releaseOffer(ctx, tx, o, "offer_expired"); err != nil {
			return o, err
		}
		if err = tx.Commit(ctx); err != nil {
			return o, err
		}
		return o, domain.ErrConflict
	}
	if blocked || d.Status != domain.PendingAssignment || d.PreparationStarted || d.DispatchDeadline == nil || !now.Before(*d.DispatchDeadline) || (accept && status != domain.Available) {
		o.Cancel(now)
		if err = releaseOffer(ctx, tx, o, "offer_cancelled"); err != nil {
			return o, err
		}
		if err = tx.Commit(ctx); err != nil {
			return o, err
		}
		return o, domain.ErrConflict
	}
	if err = o.Respond(courier, accept, now); err != nil {
		return o, err
	}
	if !accept {
		if err = releaseOffer(ctx, tx, o, "offer_declined"); err != nil {
			return o, err
		}
	} else {
		if err = saveOffer(ctx, tx, o); err != nil {
			return o, err
		}
		old := d
		if err = d.Assign(courier, now); err != nil {
			return o, err
		}
		if _, err = writeDeliveryChange(ctx, tx, old, d, events.CourierAssigned); err != nil {
			return o, err
		}
	}
	return o, tx.Commit(ctx)
}

func releaseOffer(ctx context.Context, tx pgx.Tx, o domain.CourierOffer, reason string) error {
	if err := saveOffer(ctx, tx, o); err != nil {
		return err
	}
	if o.Status == domain.OfferDeclined || o.Status == domain.OfferExpired {
		if _, err := tx.Exec(ctx, `INSERT INTO dispatch_declines(delivery_id,courier_id) VALUES($1,$2) ON CONFLICT(delivery_id,courier_id) DO UPDATE SET declined_at=clock_timestamp()`, o.DeliveryID, o.CourierID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE deliveries SET dispatch_next_at=clock_timestamp(),dispatch_reason=$2,dispatch_token=NULL,dispatch_lease_until=NULL WHERE id=$1 AND status='PENDING_ASSIGNMENT'`, o.DeliveryID, reason)
	return err
}

// Lock the delivery first, rather than selecting an offer FOR UPDATE and then
// deadlocking with acceptance/manual assignment. Multiple replicas skip owners.
func (r *Repository) ExpireOffer(ctx context.Context) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var delivery int64
	err = tx.QueryRow(ctx, `SELECT d.id FROM deliveries d JOIN courier_offers o ON o.delivery_id=d.id WHERE o.status='PENDING' AND o.expires_at<=clock_timestamp() ORDER BY o.expires_at,o.id FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(&delivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	o, err := scanOffer(tx.QueryRow(ctx, `SELECT `+offerColumns+` FROM courier_offers WHERE delivery_id=$1 AND status='PENDING' FOR UPDATE`, delivery))
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	if !o.Expire(now) {
		return false, nil
	}
	if err = releaseOffer(ctx, tx, o, "offer_expired"); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func assignmentFence(ctx context.Context, tx pgx.Tx) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(hashtextextended(current_database()||':'||current_schema()||':consumer-quarantine',0))`).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
	}
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM consumer_quarantine WHERE resolved_at IS NULL)`).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
	}
	return nil
}
