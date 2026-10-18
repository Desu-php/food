package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

type Restaurants struct{ pool *pgxpool.Pool }

var _ application.RestaurantRepository = (*Restaurants)(nil)

func NewRestaurants(pool *pgxpool.Pool) *Restaurants { return &Restaurants{pool: pool} }

func scanRestaurant(row pgx.Row) (domain.Restaurant, error) {
	var r domain.Restaurant
	err := row.Scan(&r.ID, &r.Name, &r.IsOpen, &r.CreatedAt, &r.UpdatedAt, &r.Address, &r.Latitude, &r.Longitude)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("scan restaurant: %w", err)
	}
	return r, nil
}

func (r *Restaurants) List(ctx context.Context, page application.Page) ([]domain.Restaurant, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, is_open, created_at, updated_at, address, latitude, longitude
  FROM restaurants ORDER BY id LIMIT $1 OFFSET $2`, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("query restaurants: %w", err)
	}
	defer rows.Close()
	restaurants := make([]domain.Restaurant, 0)
	for rows.Next() {
		restaurant, err := scanRestaurant(rows)
		if err != nil {
			return nil, err
		}
		restaurants = append(restaurants, restaurant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read restaurants: %w", err)
	}
	return restaurants, nil
}

func (r *Restaurants) Get(ctx context.Context, id int64) (domain.Restaurant, error) {
	return scanRestaurant(r.pool.QueryRow(ctx, `SELECT id, name, is_open, created_at, updated_at, address, latitude, longitude
  FROM restaurants WHERE id = $1`, id))
}

func (r *Restaurants) Create(ctx context.Context, restaurant domain.Restaurant) (domain.Restaurant, error) {
	return scanRestaurant(r.pool.QueryRow(ctx, `INSERT INTO restaurants (name, is_open, address, latitude, longitude)
  VALUES ($1, $2, $3, $4, $5) RETURNING id, name, is_open, created_at, updated_at, address, latitude, longitude`, restaurant.Name, restaurant.IsOpen, restaurant.Address, restaurant.Latitude, restaurant.Longitude))
}

func (r *Restaurants) Update(ctx context.Context, id int64, changes domain.RestaurantChanges) (domain.Restaurant, error) {
	// One UPDATE locks the row and preserves fields omitted by the client.
	return scanRestaurant(r.pool.QueryRow(ctx, `UPDATE restaurants
  SET name = COALESCE($2::text, name), is_open = COALESCE($3::boolean, is_open),
      address = COALESCE($4::text, address), latitude=COALESCE($5::double precision,latitude), longitude=COALESCE($6::double precision,longitude), updated_at = clock_timestamp()
  WHERE id = $1 RETURNING id, name, is_open, created_at, updated_at, address, latitude, longitude`, id, changes.Name, changes.IsOpen, changes.Address, changes.Latitude, changes.Longitude))
}
