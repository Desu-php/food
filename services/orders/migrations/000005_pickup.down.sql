-- Dropping pickup history would corrupt business facts. Roll forward once it exists.
DO $$ BEGIN
 IF EXISTS(SELECT FROM orders WHERE fulfillment_type='PICKUP') THEN RAISE EXCEPTION 'cannot remove pickup history; roll forward'; END IF;
END $$;
DROP TRIGGER delivery_saga_only ON order_sagas;
DROP FUNCTION check_delivery_saga();
DROP TRIGGER pickup_order_collection ON orders;
DROP TRIGGER pickup_confirmation_collection ON pickup_payment_confirmations;
DROP FUNCTION check_pickup_collection();
DROP TABLE pickup_payment_confirmations;
DROP TABLE pickup_codes;
DROP TRIGGER order_fulfillment_immutable ON orders;
DROP FUNCTION preserve_order_fulfillment();
ALTER TABLE orders DROP CONSTRAINT orders_fulfillment_check;
ALTER TABLE orders DROP COLUMN fulfillment_type, DROP COLUMN delivery_address, DROP COLUMN pickup_address, DROP COLUMN legacy_delivery_address, DROP COLUMN delivery_fee_minor;
ALTER TABLE orders ADD CONSTRAINT orders_payment_method_check CHECK(payment_method='POS_ON_DELIVERY');
ALTER TABLE orders DROP CONSTRAINT orders_collection_check;
ALTER TABLE orders ADD CONSTRAINT orders_collection_check CHECK((status='DELIVERED')=(payment_status='REPORTED_COLLECTED'));
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('WAITING_RESTAURANT','ACCEPTED','WAITING_COURIER','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','DELIVERED','CANCELLED'));
DROP INDEX orders_restaurant_incoming_idx;
CREATE INDEX orders_restaurant_incoming_idx ON orders(restaurant_id,id) WHERE status NOT IN ('DELIVERED','CANCELLED');
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(
 (stream='delivery_commands' AND event_type IN ('CreateDelivery','CancelDelivery')) OR
 (stream='orders' AND event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed')));
