package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

const tariffJSON = `COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.max_distance_meters) FROM delivery_tariffs t WHERE t.restaurant_id=r.id),'[]'::jsonb)`

func (r *Restaurants) GetPricing(ctx context.Context, id int64) (application.RestaurantPricing, error) {
	var p application.RestaurantPricing
	var body []byte
	err := r.pool.QueryRow(ctx, `SELECT r.delivery_enabled,r.pickup_enabled,r.minimum_delivery_subtotal_minor,r.pricing_version,`+tariffJSON+` FROM restaurants r WHERE r.id=$1`, id).Scan(&p.Settings.DeliveryEnabled, &p.Settings.PickupEnabled, &p.Settings.MinimumDeliverySubtotalMinor, &p.Settings.Version, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("load pricing: %w", err)
	}
	err = json.Unmarshal(body, &p.Tariffs)
	return p, err
}
func (r *Restaurants) UpdateSettings(ctx context.Context, id int64, s domain.DeliverySettings) (application.RestaurantPricing, error) {
	var p application.RestaurantPricing
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return p, fmt.Errorf("begin delivery settings: %w", err)
	}
	defer pricingRollback(tx)
	err = tx.QueryRow(ctx, `UPDATE restaurants SET delivery_enabled=$2,pickup_enabled=$3,minimum_delivery_subtotal_minor=$4,updated_at=clock_timestamp() WHERE id=$1 RETURNING delivery_enabled,pickup_enabled,minimum_delivery_subtotal_minor,pricing_version`, id, s.DeliveryEnabled, s.PickupEnabled, s.MinimumDeliverySubtotalMinor).Scan(&p.Settings.DeliveryEnabled, &p.Settings.PickupEnabled, &p.Settings.MinimumDeliverySubtotalMinor, &p.Settings.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("update delivery settings: %w", err)
	}
	// Read after any UPDATE lock wait. The parent lock fences all tariff writers,
	// so this statement sees the tariff set corresponding to the returned version.
	var body []byte
	if err = tx.QueryRow(ctx, `SELECT `+tariffJSON+` FROM restaurants r WHERE id=$1`, id).Scan(&body); err != nil {
		return p, fmt.Errorf("load updated delivery tariffs: %w", err)
	}
	if err = json.Unmarshal(body, &p.Tariffs); err != nil {
		return p, fmt.Errorf("decode updated delivery tariffs: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return p, fmt.Errorf("commit delivery settings: %w", err)
	}
	return p, nil
}

func pricingError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return domain.ErrTariffConflict
	}
	return err
}
func pricingRollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func (r *Restaurants) SaveTariff(ctx context.Context, t domain.DeliveryTariff) (domain.DeliveryTariff, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return t, err
	}
	defer pricingRollback(tx)
	// Serialize all tariff writers on the restaurant, including settings changes.
	tag, err := tx.Exec(ctx, `UPDATE restaurants SET updated_at=clock_timestamp() WHERE id=$1`, t.RestaurantID)
	if err != nil {
		return t, err
	}
	if tag.RowsAffected() == 0 {
		return t, domain.ErrNotFound
	}
	if t.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO delivery_tariffs(restaurant_id,max_distance_meters,delivery_fee_minor) VALUES($1,$2,$3) RETURNING id,created_at,updated_at`, t.RestaurantID, t.MaxDistanceMeters, t.DeliveryFeeMinor).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `UPDATE delivery_tariffs SET max_distance_meters=$3,delivery_fee_minor=$4,updated_at=clock_timestamp() WHERE restaurant_id=$1 AND id=$2 RETURNING created_at,updated_at`, t.RestaurantID, t.ID, t.MaxDistanceMeters, t.DeliveryFeeMinor).Scan(&t.CreatedAt, &t.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return t, domain.ErrTariffNotFound
	}
	if err != nil {
		return t, pricingError(err)
	}
	return t, tx.Commit(ctx)
}
func (r *Restaurants) DeleteTariff(ctx context.Context, restaurant, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer pricingRollback(tx)
	tag, err := tx.Exec(ctx, `UPDATE restaurants SET updated_at=clock_timestamp() WHERE id=$1`, restaurant)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	tag, err = tx.Exec(ctx, `DELETE FROM delivery_tariffs WHERE restaurant_id=$1 AND id=$2`, restaurant, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTariffNotFound
	}
	return tx.Commit(ctx)
}
