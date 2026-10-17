-- Rollback is safe only before fulfillment starts; never erase fulfilled orders.
DO $$ BEGIN
 IF EXISTS (SELECT FROM orders WHERE status NOT IN ('WAITING_RESTAURANT','CANCELLED')) OR
 EXISTS (SELECT FROM outbox_events WHERE event_type NOT IN ('OrderCreated','OrderCancelled')) OR
 EXISTS (SELECT FROM order_deliveries) THEN
 RAISE EXCEPTION 'fulfillment data exists; restore a backup or roll forward';
 END IF;
END $$;
DROP TABLE order_deliveries;
DROP TABLE delivery_inbox;
DROP INDEX orders_restaurant_incoming_idx;
ALTER TABLE orders DROP CONSTRAINT orders_collection_check;
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
UPDATE orders SET status='PENDING_PAYMENT' WHERE status='WAITING_RESTAURANT';
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('PENDING_PAYMENT','CANCELLED'));
ALTER TABLE orders DROP COLUMN payment_status, DROP COLUMN payment_method;
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('OrderCreated','OrderCancelled'));
