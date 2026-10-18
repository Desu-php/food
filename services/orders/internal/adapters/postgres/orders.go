package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/food/contracts/geo"
	events "example.com/food/contracts/orders/v1"
	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

type Orders struct {
	pool        *pgxpool.Pool
	fulfillment *application.Fulfillment
}

var _ application.OrderRepository = (*Orders)(nil)

func NewOrders(pool *pgxpool.Pool, fulfillment ...*application.Fulfillment) *Orders {
	f := application.NewFulfillment(application.DefaultSagaConfig())
	if len(fulfillment) > 0 {
		f = fulfillment[0]
	}
	return &Orders{pool: pool, fulfillment: f}
}

func (r *Orders) FindCreation(ctx context.Context, customerID int64, key, hash string) (domain.Order, bool, error) {
	var storedHash string
	var body []byte
	err := r.pool.QueryRow(ctx, `SELECT request_hash,response FROM idempotency_keys WHERE customer_id=$1 AND key=$2`, customerID, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, false, nil
	}
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("find creation: %w", err)
	}
	if storedHash != hash {
		return domain.Order{}, false, application.ErrIdempotencyConflict
	}
	var state domain.State
	if err := json.Unmarshal(body, &state); err != nil {
		return domain.Order{}, false, fmt.Errorf("decode creation result: %w", err)
	}
	order, err := domain.Restore(state)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("restore creation result: %w", err)
	}
	return order, true, nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (r *Orders) Create(ctx context.Context, key, hash string, order domain.Order) (domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("begin creation: %w", err)
	}
	defer rollback(tx)
	state := order.Snapshot()
	var latitude, longitude *float64
	if state.RestaurantLocation != nil {
		latitude = &state.RestaurantLocation.Latitude
		longitude = &state.RestaurantLocation.Longitude
	}
	err = tx.QueryRow(ctx, `INSERT INTO orders(customer_id,restaurant_id,status,currency,subtotal_minor,created_at,updated_at,fulfillment_type,delivery_address,pickup_address,payment_method,latitude,longitude)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id,created_at,updated_at`, state.CustomerID, state.RestaurantID, state.Status, state.Currency, state.SubtotalMinor, state.CreatedAt, state.UpdatedAt, state.FulfillmentType, state.DeliveryAddress, state.PickupAddress, state.PaymentMethod, latitude, longitude).Scan(&state.ID, &state.CreatedAt, &state.UpdatedAt)
	if err != nil {
		return domain.Order{}, fmt.Errorf("insert order: %w", err)
	}
	for i := range state.Items {
		p := &state.Items[i]
		p.OrderID = state.ID
		err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,menu_item_id,name,quantity,unit_price_minor,total_price_minor)
 VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, p.OrderID, p.MenuItemID, p.Name, p.Quantity, p.UnitPriceMinor, p.TotalPriceMinor).Scan(&p.ID)
		if err != nil {
			return domain.Order{}, fmt.Errorf("insert order item: %w", err)
		}
	}
	order, err = domain.Restore(state)
	if err != nil {
		return domain.Order{}, fmt.Errorf("restore created order: %w", err)
	}
	if state.FulfillmentType == domain.Delivery {
		if err := saveSaga(ctx, tx, domain.NewSaga(state.ID, state.CreatedAt)); err != nil {
			return domain.Order{}, err
		}
	}
	if err := insertEvent(ctx, tx, application.OrderEvent(ctx, state, events.OrderCreated)); err != nil {
		return domain.Order{}, err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return domain.Order{}, fmt.Errorf("encode creation: %w", err)
	}
	result, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(customer_id,key,request_hash,order_id,response)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(customer_id,key) DO NOTHING`, state.CustomerID, key, hash, state.ID, body)
	if err != nil {
		return domain.Order{}, fmt.Errorf("save creation result: %w", err)
	}
	if result.RowsAffected() == 0 {
		// The unique constraint waits for the winner's commit. Discard the losing
		// aggregate before loading that winner's immutable creation response.
		if err := tx.Rollback(ctx); err != nil {
			return domain.Order{}, fmt.Errorf("rollback duplicate creation: %w", err)
		}
		previous, found, err := r.FindCreation(ctx, state.CustomerID, key, hash)
		if err != nil {
			return domain.Order{}, err
		}
		if !found {
			return domain.Order{}, errors.New("creation result disappeared")
		}
		return previous, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, fmt.Errorf("commit creation: %w", err)
	}
	return order, nil
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func load(ctx context.Context, q querier, customerID, id int64, lock bool) (domain.Order, error) {
	query := `SELECT o.id,o.version,o.customer_id,o.restaurant_id,o.status,o.courier_id,o.cancellation_requested,o.payment_method,o.payment_status,o.currency,o.subtotal_minor,o.created_at,o.updated_at,o.fulfillment_type,o.delivery_address,o.pickup_address,o.legacy_delivery_address,o.delivery_fee_minor,o.latitude,o.longitude,
 i.id,i.order_id,i.menu_item_id,i.name,i.quantity,i.unit_price_minor,i.total_price_minor
 FROM orders o JOIN order_items i ON i.order_id=o.id WHERE o.customer_id=$1 AND o.id=$2 ORDER BY i.id`
	if lock {
		query += " FOR UPDATE OF o"
	}
	rows, err := q.Query(ctx, query, customerID, id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("query order: %w", err)
	}
	defer rows.Close()
	orders, err := readOrders(rows)
	if err != nil {
		return domain.Order{}, err
	}
	if len(orders) == 0 {
		return domain.Order{}, domain.ErrNotFound
	}
	return orders[0], nil
}

func readOrders(rows pgx.Rows) ([]domain.Order, error) {
	defer rows.Close()
	result := []domain.Order{}
	var state domain.State
	appendOrder := func() error {
		if state.ID == 0 {
			return nil
		}
		o, err := domain.Restore(state)
		if err != nil {
			return fmt.Errorf("restore order: %w", err)
		}
		result = append(result, o)
		return nil
	}
	for rows.Next() {
		var next domain.State
		var latitude, longitude *float64
		var p domain.OrderItem
		if err := rows.Scan(&next.ID, &next.Version, &next.CustomerID, &next.RestaurantID, &next.Status, &next.CourierID, &next.CancellationRequested, &next.PaymentMethod, &next.PaymentStatus, &next.Currency, &next.SubtotalMinor, &next.CreatedAt, &next.UpdatedAt, &next.FulfillmentType, &next.DeliveryAddress, &next.PickupAddress, &next.LegacyDeliveryAddress, &next.DeliveryFeeMinor, &latitude, &longitude, &p.ID, &p.OrderID, &p.MenuItemID, &p.Name, &p.Quantity, &p.UnitPriceMinor, &p.TotalPriceMinor); err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		if latitude != nil && longitude != nil {
			next.RestaurantLocation = &geo.Point{Latitude: *latitude, Longitude: *longitude}
		}
		if next.ID != state.ID {
			if err := appendOrder(); err != nil {
				return nil, err
			}
			state = next
		}
		state.Items = append(state.Items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read orders: %w", err)
	}
	if err := appendOrder(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Orders) Get(ctx context.Context, customerID, id int64) (domain.Order, error) {
	return load(ctx, r.pool, customerID, id, false)
}
func (r *Orders) Cancel(ctx context.Context, customerID, id int64, change application.FulfillmentMutation) (domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("begin cancellation: %w", err)
	}
	defer rollback(tx)
	o, err := load(ctx, tx, customerID, id, true)
	if err != nil {
		return domain.Order{}, err
	}
	s, err := loadOrderSaga(ctx, tx, o)
	if err != nil {
		return domain.Order{}, err
	}
	outcome, err := change(&o, &s)
	if err != nil {
		return domain.Order{}, err
	}
	if err = persistFulfillment(ctx, tx, o, s, outcome); err != nil {
		return domain.Order{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Order{}, fmt.Errorf("commit cancellation: %w", err)
	}
	return o, nil
}
