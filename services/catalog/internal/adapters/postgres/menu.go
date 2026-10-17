package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
)

type Menu struct{ pool *pgxpool.Pool }

var _ application.MenuRepository = (*Menu)(nil)

func NewMenu(pool *pgxpool.Pool) *Menu { return &Menu{pool: pool} }

const categoryColumns = "id, restaurant_id, name, sort_order, created_at, updated_at"
const itemColumns = "id, restaurant_id, category_id, name, description, price_minor, currency, is_available, created_at, updated_at"

func menuDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.ConstraintName {
		case "menu_categories_restaurant_fk", "menu_items_restaurant_fk":
			return domain.ErrNotFound
		case "menu_items_category_fk":
			return domain.ErrCategoryNotFound
		case "menu_categories_name_check", "menu_items_name_check":
			return domain.ErrInvalidName
		case "menu_categories_sort_order_check":
			return domain.ErrInvalidSortOrder
		case "menu_items_price_check":
			return domain.ErrInvalidPrice
		case "menu_items_currency_check":
			return domain.ErrInvalidCurrency
		case "menu_items_description_check":
			return domain.ErrInvalidDescription
		}
	}
	return fmt.Errorf("menu database operation: %w", err)
}

func scanCategory(row pgx.Row) (domain.MenuCategory, error) {
	var c domain.MenuCategory
	err := row.Scan(&c.ID, &c.RestaurantID, &c.Name, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MenuCategory{}, domain.ErrCategoryNotFound
	}
	if err != nil {
		return domain.MenuCategory{}, menuDatabaseError(err)
	}
	return c, nil
}

func scanItem(row pgx.Row) (domain.MenuItem, error) {
	var i domain.MenuItem
	err := row.Scan(&i.ID, &i.RestaurantID, &i.CategoryID, &i.Name, &i.Description, &i.PriceMinor, &i.Currency, &i.IsAvailable, &i.CreatedAt, &i.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MenuItem{}, domain.ErrItemNotFound
	}
	if err != nil {
		return domain.MenuItem{}, menuDatabaseError(err)
	}
	return i, nil
}

func (r *Menu) GetCategory(ctx context.Context, id int64) (domain.MenuCategory, error) {
	return scanCategory(r.pool.QueryRow(ctx, "SELECT "+categoryColumns+" FROM menu_categories WHERE id=$1", id))
}

func (r *Menu) CreateCategory(ctx context.Context, c domain.MenuCategory) (domain.MenuCategory, error) {
	return scanCategory(r.pool.QueryRow(ctx, `INSERT INTO menu_categories (restaurant_id,name,sort_order)
  VALUES ($1,$2,$3) RETURNING `+categoryColumns, c.RestaurantID, c.Name, c.SortOrder))
}

func (r *Menu) UpdateCategory(ctx context.Context, restaurantID, categoryID int64, c domain.MenuCategoryChanges) (domain.MenuCategory, error) {
	return scanCategory(r.pool.QueryRow(ctx, `UPDATE menu_categories
  SET name=COALESCE($3::text,name),sort_order=COALESCE($4::integer,sort_order),updated_at=clock_timestamp()
  WHERE restaurant_id=$1 AND id=$2 RETURNING `+categoryColumns, restaurantID, categoryID, c.Name, c.SortOrder))
}

func (r *Menu) CreateItem(ctx context.Context, i domain.MenuItem) (domain.MenuItem, error) {
	return scanItem(r.pool.QueryRow(ctx, `INSERT INTO menu_items (restaurant_id,category_id,name,description,price_minor,currency,is_available)
  VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+itemColumns, i.RestaurantID, i.CategoryID, i.Name, i.Description, i.PriceMinor, i.Currency, i.IsAvailable))
}

func (r *Menu) UpdateItem(ctx context.Context, restaurantID, itemID int64, c domain.MenuItemChanges) (domain.MenuItem, error) {
	// No read-modify-write: a single row-locked UPDATE preserves omitted fields.
	return scanItem(r.pool.QueryRow(ctx, `UPDATE menu_items SET
  category_id=COALESCE($3::bigint,category_id),name=COALESCE($4::text,name),
  description=COALESCE($5::text,description),price_minor=COALESCE($6::bigint,price_minor),
  currency=COALESCE($7::text,currency),is_available=COALESCE($8::boolean,is_available),updated_at=clock_timestamp()
  WHERE restaurant_id=$1 AND id=$2 AND deleted_at IS NULL RETURNING `+itemColumns,
		restaurantID, itemID, c.CategoryID, c.Name, c.Description, c.PriceMinor, c.Currency, c.IsAvailable))
}

func (r *Menu) DeleteItem(ctx context.Context, restaurantID, itemID int64) error {
	result, err := r.pool.Exec(ctx, `UPDATE menu_items SET deleted_at=clock_timestamp(),updated_at=clock_timestamp()
  WHERE restaurant_id=$1 AND id=$2 AND deleted_at IS NULL`, restaurantID, itemID)
	if err != nil {
		return menuDatabaseError(err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrItemNotFound
	}
	return nil
}

func (r *Menu) GetMenu(ctx context.Context, restaurantID int64) (application.RestaurantMenu, error) {
	// One statement gives one MVCC snapshot, including existence and empty categories.
	rows, err := r.pool.Query(ctx, `SELECT r.id,c.id,c.name,c.sort_order,c.created_at,c.updated_at,
  i.id,i.name,i.description,i.price_minor,i.currency,i.is_available,i.created_at,i.updated_at
  FROM restaurants r
  LEFT JOIN menu_categories c ON c.restaurant_id=r.id
  LEFT JOIN menu_items i ON i.restaurant_id=c.restaurant_id AND i.category_id=c.id AND i.deleted_at IS NULL
  WHERE r.id=$1 ORDER BY c.sort_order,c.id,i.id`, restaurantID)
	if err != nil {
		return application.RestaurantMenu{}, menuDatabaseError(err)
	}
	defer rows.Close()
	menu := application.RestaurantMenu{RestaurantID: restaurantID, Categories: make([]application.MenuCategoryGroup, 0)}
	found := false
	for rows.Next() {
		var id int64
		var categoryID, itemID *int64
		var categoryName, itemName, description, currency *string
		var sortOrder *int32
		var price *int64
		var available *bool
		var categoryCreated, categoryUpdated, itemCreated, itemUpdated *time.Time
		if err := rows.Scan(&id, &categoryID, &categoryName, &sortOrder, &categoryCreated, &categoryUpdated,
			&itemID, &itemName, &description, &price, &currency, &available, &itemCreated, &itemUpdated); err != nil {
			return application.RestaurantMenu{}, menuDatabaseError(err)
		}
		found = true
		if categoryID == nil {
			continue
		}
		if len(menu.Categories) == 0 || menu.Categories[len(menu.Categories)-1].Category.ID != *categoryID {
			menu.Categories = append(menu.Categories, application.MenuCategoryGroup{
				Category: domain.MenuCategory{ID: *categoryID, RestaurantID: id, Name: *categoryName, SortOrder: *sortOrder, CreatedAt: *categoryCreated, UpdatedAt: *categoryUpdated},
				Items:    make([]domain.MenuItem, 0),
			})
		}
		if itemID == nil {
			continue
		}
		group := &menu.Categories[len(menu.Categories)-1]
		group.Items = append(group.Items, domain.MenuItem{ID: *itemID, RestaurantID: id, CategoryID: *categoryID, Name: *itemName, Description: *description,
			PriceMinor: *price, Currency: *currency, IsAvailable: *available, CreatedAt: *itemCreated, UpdatedAt: *itemUpdated})
	}
	if err := rows.Err(); err != nil {
		return application.RestaurantMenu{}, menuDatabaseError(err)
	}
	if !found {
		return application.RestaurantMenu{}, domain.ErrNotFound
	}
	return menu, nil
}
