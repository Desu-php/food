package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool} }
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func conflict(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return domain.ErrConflict
	}
	return err
}
func (r *Repository) CreateCourier(ctx context.Context, c domain.Courier) (domain.Courier, error) {
	err := r.pool.QueryRow(ctx, `INSERT INTO couriers(name,status,created_at,updated_at) VALUES($1,$2,$3,$3) RETURNING id`, c.Name, c.Status, c.CreatedAt).Scan(&c.ID)
	return c, err
}
func (r *Repository) Couriers(ctx context.Context, after int64, limit int) ([]domain.Courier, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,name,status,created_at,updated_at FROM couriers WHERE id>$1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Courier{}
	for rows.Next() {
		var c domain.Courier
		if err = rows.Scan(&c.ID, &c.Name, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

const deliveryColumns = `d.id,d.order_id,COALESCE(d.courier_id,0),d.status,d.amount_to_collect_minor,d.currency,d.version,d.order_version,d.ready,d.preparation_started,d.created_at,d.updated_at,d.delivery_address,d.latitude,d.longitude,d.dispatch_deadline,d.dispatch_attempts,d.dispatch_reason,d.dispatch_next_at,d.dispatch_paused`

func scanDelivery(row pgx.Row) (domain.Delivery, error) {
	var d domain.Delivery
	var latitude, longitude *float64
	err := row.Scan(&d.ID, &d.OrderID, &d.CourierID, &d.Status, &d.AmountToCollectMinor, &d.Currency, &d.Version, &d.OrderVersion, &d.Ready, &d.PreparationStarted, &d.CreatedAt, &d.UpdatedAt, &d.DeliveryAddress, &latitude, &longitude, &d.DispatchDeadline, &d.DispatchAttempts, &d.DispatchReason, &d.DispatchNextAt, &d.DispatchPaused)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, domain.ErrNotFound
	}
	if latitude != nil && longitude != nil {
		d.RestaurantLocation = &geo.Point{Latitude: *latitude, Longitude: *longitude}
	}
	return d, err
}
func loadDelivery(ctx context.Context, tx pgx.Tx, id int64) (domain.Delivery, error) {
	d, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM deliveries d WHERE d.id=$1 FOR UPDATE`, id))
	if err != nil {
		return d, err
	}
	if d.Status == domain.Delivered {
		p := &domain.Confirmation{}
		err = tx.QueryRow(ctx, `SELECT order_id,delivery_id,amount_minor,currency,payment_method,receipt_reference,confirmed_by,confirmed_at,id FROM payment_confirmations WHERE delivery_id=$1`, id).Scan(&p.OrderID, &p.DeliveryID, &p.AmountMinor, &p.Currency, &p.PaymentMethod, &p.ReceiptReference, &p.ConfirmedBy, &p.ConfirmedAt, &p.ID)
		d.Confirmation = p
	}
	return d, err
}
func (r *Repository) Get(ctx context.Context, courier, id int64) (domain.Delivery, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delivery{}, err
	}
	defer rollback(tx)
	d, err := loadDelivery(ctx, tx, id)
	if err != nil {
		return d, err
	}
	if courier > 0 && d.CourierID != courier {
		return domain.Delivery{}, domain.ErrNotFound
	}
	return d, tx.Commit(ctx)
}
func (r *Repository) List(ctx context.Context, courier, after int64, limit int) ([]domain.Delivery, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM deliveries d WHERE d.id>$1 AND ($2::bigint=0 OR d.courier_id=$2) ORDER BY d.id LIMIT $3`, after, courier, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func insertDeliveryEvent(ctx context.Context, tx pgx.Tx, d domain.Delivery, kind string) error {
	e, body, err := application.EncodeEvent(ctx, d, kind)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,aggregate_version,event_type,correlation_id,body) VALUES($1,$2,$3,$4,$5,$6)`, e.EventID, e.AggregateID, e.AggregateVersion, e.EventType, e.CorrelationID, body)
	return err
}
func (r *Repository) Change(ctx context.Context, id int64, kind string, change func(*domain.Delivery) error) (domain.Delivery, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Delivery{}, fmt.Errorf("begin delivery change: %w", err)
	}
	defer rollback(tx)
	d, err := loadDelivery(ctx, tx, id)
	if err != nil {
		return d, err
	}
	old := d
	if err = change(&d); err != nil {
		return domain.Delivery{}, err
	}
	if old.Version == d.Version {
		return d, tx.Commit(ctx)
	}
	d, err = writeDeliveryChange(ctx, tx, old, d, kind)
	if err != nil {
		return domain.Delivery{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Delivery{}, fmt.Errorf("commit delivery: %w", err)
	}
	return d, nil
}
func saveDelivery(ctx context.Context, tx pgx.Tx, d domain.Delivery) error {
	_, err := tx.Exec(ctx, `UPDATE deliveries SET courier_id=NULLIF($2,0),status=$3,version=$4,updated_at=$5,preparation_started=$6,ready=$7,order_version=$8,dispatch_deadline=$9,
 dispatch_token=CASE WHEN $3<>'PENDING_ASSIGNMENT' OR $6 THEN NULL ELSE dispatch_token END,
 dispatch_lease_until=CASE WHEN $3<>'PENDING_ASSIGNMENT' OR $6 THEN NULL ELSE dispatch_lease_until END,
 dispatch_paused=CASE WHEN $3='ASSIGNED' THEN false ELSE dispatch_paused END,
 dispatch_reason=CASE WHEN $3='ASSIGNED' THEN 'assigned' WHEN $3='CANCELLED' THEN 'cancelled' WHEN $6 AND $3='PENDING_ASSIGNMENT' THEN 'manual_assignment_required_after_preparation' ELSE dispatch_reason END
 WHERE id=$1`, d.ID, d.CourierID, d.Status, d.Version, d.UpdatedAt, d.PreparationStarted, d.Ready, d.OrderVersion, d.DispatchDeadline)
	return conflict(err)
}
func writeDeliveryChange(ctx context.Context, tx pgx.Tx, old, d domain.Delivery, kind string) (domain.Delivery, error) {
	if old.Status == domain.PendingAssignment && d.Status == domain.Assigned {
		d.DispatchReason = "assigned"
		d.DispatchPaused = false
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM couriers WHERE id=$1 FOR UPDATE`, d.CourierID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return d, domain.ErrNotFound
		}
		if err != nil {
			return d, err
		}
		if status != domain.Available {
			return d, domain.ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE couriers SET status='BUSY',updated_at=$2 WHERE id=$1`, d.CourierID, d.UpdatedAt); err != nil {
			return d, err
		}
	}
	if old.CourierID > 0 && (d.CourierID != old.CourierID || d.Status == domain.Delivered || d.Status == domain.Cancelled) && (old.Status == domain.Assigned || old.Status == domain.PickedUp) {
		if _, err := tx.Exec(ctx, `UPDATE couriers SET status='AVAILABLE',updated_at=$2 WHERE id=$1`, old.CourierID, d.UpdatedAt); err != nil {
			return d, err
		}
	}
	if old.Status == domain.Assigned && d.Status == domain.PendingAssignment {
		d.DispatchAttempts = 0
		d.DispatchPaused = false
		d.DispatchReason = ""
		d.DispatchNextAt = time.Now()
		if _, err := tx.Exec(ctx, `INSERT INTO dispatch_declines(delivery_id,courier_id) VALUES($1,$2) ON CONFLICT(delivery_id,courier_id) DO UPDATE SET declined_at=clock_timestamp()`, d.ID, old.CourierID); err != nil {
			return d, err
		}
		if _, err := tx.Exec(ctx, `UPDATE deliveries SET dispatch_attempts=0,dispatch_paused=false,dispatch_next_at=now(),dispatch_reason='',dispatch_token=NULL,dispatch_lease_until=NULL WHERE id=$1`, d.ID); err != nil {
			return d, err
		}
	}
	if err := saveDelivery(ctx, tx, d); err != nil {
		return d, err
	}
	if d.Confirmation != nil && old.Status != domain.Delivered {
		p := d.Confirmation
		err := tx.QueryRow(ctx, `INSERT INTO payment_confirmations(delivery_id,order_id,amount_minor,currency,payment_method,receipt_reference,confirmed_by,confirmed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.DeliveryID, p.OrderID, p.AmountMinor, p.Currency, p.PaymentMethod, p.ReceiptReference, p.ConfirmedBy, p.ConfirmedAt).Scan(&p.ID)
		if err != nil {
			return d, err
		}
	}
	if err := insertDeliveryEvent(ctx, tx, d, kind); err != nil {
		return d, err
	}
	return d, nil
}
func (r *Repository) SetCourierAvailability(ctx context.Context, id int64, status string) (domain.Courier, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Courier{}, err
	}
	defer rollback(tx)
	var c domain.Courier
	err = tx.QueryRow(ctx, `SELECT id,name,status,created_at,updated_at FROM couriers WHERE id=$1 FOR UPDATE`, id).Scan(&c.ID, &c.Name, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err = c.SetAvailability(status, time.Now()); err != nil {
		return c, err
	}
	if _, err = tx.Exec(ctx, `UPDATE couriers SET status=$2,updated_at=$3 WHERE id=$1`, id, c.Status, c.UpdatedAt); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (r *Repository) AvailableCouriers(ctx context.Context, after int64, limit int) ([]domain.Courier, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,name,status,created_at,updated_at FROM couriers WHERE id>$1 AND status='AVAILABLE' ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Courier{}
	for rows.Next() {
		var c domain.Courier
		if err = rows.Scan(&c.ID, &c.Name, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
