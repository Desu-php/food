package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"example.com/food/orders/internal/application"
	"example.com/food/orders/internal/domain"
)

func (r *Orders) ListIncoming(ctx context.Context, restaurant, after int64, limit int) ([]domain.Order, error) {
	// Page aggregates before joining items; one statement gives a consistent snapshot.
	rows, err := r.pool.Query(ctx, `SELECT o.id,o.version,o.customer_id,o.restaurant_id,o.status,o.courier_id,o.cancellation_requested,o.payment_method,o.payment_status,o.currency,o.subtotal_minor,o.created_at,o.updated_at,o.fulfillment_type,o.delivery_address,o.pickup_address,o.legacy_delivery_address,o.delivery_fee_minor,o.latitude,o.longitude,
 i.id,i.order_id,i.menu_item_id,i.name,i.quantity,i.unit_price_minor,i.total_price_minor
 FROM (SELECT * FROM orders WHERE restaurant_id=$1 AND id>$2 AND status NOT IN ('DELIVERED','PICKED_UP_BY_CUSTOMER','CANCELLED') ORDER BY id LIMIT $3) o
 JOIN order_items i ON i.order_id=o.id ORDER BY o.id,i.id`, restaurant, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list incoming: %w", err)
	}
	return readOrders(rows)
}

func (r *Orders) ChangeRestaurant(ctx context.Context, restaurant, id int64, change application.FulfillmentMutation) (domain.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, err
	}
	defer rollback(tx)
	var customer int64
	err = tx.QueryRow(ctx, `SELECT customer_id FROM orders WHERE restaurant_id=$1 AND id=$2 FOR UPDATE`, restaurant, id).Scan(&customer)
	if err == pgx.ErrNoRows {
		return domain.Order{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Order{}, err
	}
	o, err := load(ctx, tx, customer, id, false)
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
		return domain.Order{}, err
	}
	return o, nil
}
func saveOrder(ctx context.Context, tx pgx.Tx, o domain.Order, kind string) error {
	s := o.Snapshot()
	if _, err := tx.Exec(ctx, `UPDATE orders SET status=$2,payment_status=$3,version=$4,updated_at=$5,courier_id=$6,cancellation_requested=$7 WHERE id=$1`, s.ID, s.Status, s.PaymentStatus, s.Version, s.UpdatedAt, s.CourierID, s.CancellationRequested); err != nil {
		return fmt.Errorf("save fulfillment: %w", err)
	}
	return insertEvent(ctx, tx, application.OrderEvent(ctx, s, kind))
}
