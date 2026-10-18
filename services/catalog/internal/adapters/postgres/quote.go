package postgres

import (
	"context"
	"fmt"

	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
)

var _ application.QuoteRepository = (*Menu)(nil)

func (r *Menu) LoadQuote(ctx context.Context, restaurantID int64, ids []int64) (domain.Restaurant, []domain.MenuItem, error) {
	// One statement: restaurant availability and all requested items share a snapshot.
	// Join on item ID alone so the application can distinguish wrong ownership.
	rows, err := r.pool.Query(ctx, `SELECT r.id,r.is_open,r.address,r.latitude,r.longitude,i.id,i.restaurant_id,i.name,i.price_minor,i.currency,i.is_available
 FROM restaurants r CROSS JOIN unnest($2::bigint[]) WITH ORDINALITY requested(id,position)
 LEFT JOIN menu_items i ON i.id=requested.id AND i.deleted_at IS NULL
 WHERE r.id=$1 ORDER BY requested.position`, restaurantID, ids)
	if err != nil {
		return domain.Restaurant{}, nil, fmt.Errorf("query quote: %w", err)
	}
	defer rows.Close()
	var restaurant domain.Restaurant
	menu := make([]domain.MenuItem, 0, len(ids))
	missing := false
	for rows.Next() {
		var id, owner, price *int64
		var name, currency *string
		var available *bool
		if err := rows.Scan(&restaurant.ID, &restaurant.IsOpen, &restaurant.Address, &restaurant.Latitude, &restaurant.Longitude, &id, &owner, &name, &price, &currency, &available); err != nil {
			return domain.Restaurant{}, nil, fmt.Errorf("scan quote: %w", err)
		}
		if id == nil {
			missing = true
			continue
		}
		menu = append(menu, domain.MenuItem{ID: *id, RestaurantID: *owner, Name: *name, PriceMinor: *price, Currency: *currency, IsAvailable: *available})
	}
	if err := rows.Err(); err != nil {
		return domain.Restaurant{}, nil, fmt.Errorf("read quote: %w", err)
	}
	if restaurant.ID == 0 {
		return restaurant, nil, domain.ErrNotFound
	}
	if missing {
		return restaurant, nil, domain.ErrItemNotFound
	}
	return restaurant, menu, nil
}
