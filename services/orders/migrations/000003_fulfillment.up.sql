-- Preserve historical Outbox bodies and immutable idempotency responses.
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD COLUMN payment_method TEXT NOT NULL DEFAULT 'POS_ON_DELIVERY' CHECK(payment_method='POS_ON_DELIVERY');
ALTER TABLE orders ADD COLUMN payment_status TEXT NOT NULL DEFAULT 'UNPAID' CHECK(payment_status IN ('UNPAID','REPORTED_COLLECTED'));
UPDATE orders SET status='WAITING_RESTAURANT' WHERE status='PENDING_PAYMENT';
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('WAITING_RESTAURANT','ACCEPTED','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','DELIVERED','CANCELLED'));
ALTER TABLE orders ADD CONSTRAINT orders_collection_check CHECK((status='DELIVERED')=(payment_status='REPORTED_COLLECTED'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered'));
CREATE INDEX orders_restaurant_incoming_idx ON orders(restaurant_id,id) WHERE status NOT IN ('DELIVERED','CANCELLED');
CREATE TABLE delivery_inbox (
 event_id UUID PRIMARY KEY,
 delivery_id BIGINT NOT NULL CHECK(delivery_id>0),
 aggregate_version BIGINT NOT NULL CHECK(aggregate_version>0),
 fingerprint BYTEA NOT NULL,
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(delivery_id,aggregate_version)
);
CREATE TABLE order_deliveries (
 order_id BIGINT PRIMARY KEY REFERENCES orders(id),
 delivery_id BIGINT NOT NULL UNIQUE CHECK(delivery_id>0),
 version BIGINT NOT NULL CHECK(version>0),
 payment_confirmation JSONB
);
