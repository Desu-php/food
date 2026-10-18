package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) TrackingState(ctx context.Context, customer, order int64) (application.TrackingState, error) {
	var s application.TrackingState
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(d.courier_id,0),d.version,d.status FROM deliveries d
 JOIN delivery_requests r ON r.order_id=d.order_id WHERE d.order_id=$1 AND r.customer_id=$2`, order, customer).Scan(&s.CourierID, &s.Version, &s.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, domain.ErrNotFound
	}
	if err != nil {
		return s, fmt.Errorf("read tracking state: %w", err)
	}
	return s, nil
}

func (r *Repository) CourierLocationAllowed(ctx context.Context, courier int64) error {
	var status string
	err := r.pool.QueryRow(ctx, `SELECT status FROM couriers WHERE id=$1`, courier).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read courier location permission: %w", err)
	}
	if status != domain.Available && status != domain.Busy {
		return domain.ErrConflict
	}
	return nil
}
