-- Existing orders were delivery-only. No address, payment or fulfillment facts are invented.
ALTER TABLE orders ADD COLUMN fulfillment_type TEXT NOT NULL DEFAULT 'DELIVERY';
ALTER TABLE orders ADD COLUMN delivery_address TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN pickup_address TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN legacy_delivery_address BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE orders ALTER COLUMN legacy_delivery_address SET DEFAULT false;
ALTER TABLE orders ALTER COLUMN fulfillment_type DROP DEFAULT;
ALTER TABLE orders ADD COLUMN delivery_fee_minor BIGINT NOT NULL DEFAULT 0 CHECK(delivery_fee_minor=0);
ALTER TABLE orders DROP CONSTRAINT orders_payment_method_check;
ALTER TABLE orders DROP CONSTRAINT orders_collection_check;
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('WAITING_RESTAURANT','ACCEPTED','WAITING_COURIER','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','DELIVERED','PICKED_UP_BY_CUSTOMER','CANCELLED'));
ALTER TABLE orders ADD CONSTRAINT orders_collection_check CHECK((status IN ('DELIVERED','PICKED_UP_BY_CUSTOMER'))=(payment_status='REPORTED_COLLECTED'));
ALTER TABLE orders ADD CONSTRAINT orders_fulfillment_check CHECK(
 (fulfillment_type='DELIVERY' AND payment_method='POS_ON_DELIVERY' AND pickup_address='' AND status<>'PICKED_UP_BY_CUSTOMER' AND
   ((NOT legacy_delivery_address AND char_length(btrim(delivery_address)) BETWEEN 1 AND 500 AND delivery_address=btrim(delivery_address)) OR (legacy_delivery_address AND delivery_address=''))) OR
 (fulfillment_type='PICKUP' AND payment_method='POS_AT_PICKUP' AND delivery_address='' AND NOT legacy_delivery_address AND
  char_length(btrim(pickup_address)) BETWEEN 1 AND 500 AND pickup_address=btrim(pickup_address) AND courier_id=0 AND NOT cancellation_requested AND status NOT IN ('WAITING_COURIER','ON_THE_WAY','DELIVERED')));
CREATE FUNCTION preserve_order_fulfillment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.fulfillment_type,NEW.delivery_address,NEW.pickup_address,NEW.legacy_delivery_address,NEW.payment_method,NEW.delivery_fee_minor)
 IS DISTINCT FROM (OLD.fulfillment_type,OLD.delivery_address,OLD.pickup_address,OLD.legacy_delivery_address,OLD.payment_method,OLD.delivery_fee_minor) THEN
  RAISE EXCEPTION 'order fulfillment is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER order_fulfillment_immutable BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION preserve_order_fulfillment();
DROP INDEX orders_restaurant_incoming_idx;
CREATE INDEX orders_restaurant_incoming_idx ON orders(restaurant_id,id) WHERE status NOT IN ('DELIVERED','PICKED_UP_BY_CUSTOMER','CANCELLED');
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(
 (stream='delivery_commands' AND event_type IN ('CreateDelivery','CancelDelivery')) OR
 (stream='orders' AND event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderPickedUpByCustomer','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed')));
CREATE TABLE pickup_codes (
 order_id BIGINT PRIMARY KEY REFERENCES orders(id),
 code_hash BYTEA NOT NULL CHECK(octet_length(code_hash)=32),
 expires_at TIMESTAMPTZ NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 blocked_until TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE pickup_payment_confirmations (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 order_id BIGINT NOT NULL UNIQUE REFERENCES orders(id),
 confirmed_by BIGINT NOT NULL CHECK(confirmed_by>0),
 amount_minor BIGINT NOT NULL CHECK(amount_minor>0),
 currency TEXT NOT NULL CHECK(currency='TJS'),
 payment_method TEXT NOT NULL CHECK(payment_method='POS_AT_PICKUP'),
 confirmed_at TIMESTAMPTZ NOT NULL,
 receipt_reference TEXT NOT NULL DEFAULT '' CHECK(char_length(receipt_reference)<=200)
);
-- Protect atomic completion even if a future writer forgets to insert the confirmation.
CREATE FUNCTION check_pickup_collection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE oid BIGINT; o orders; p pickup_payment_confirmations;
BEGIN
 IF TG_TABLE_NAME='orders' THEN oid:=NEW.id;
 ELSIF TG_OP='DELETE' THEN oid:=OLD.order_id;
 ELSE oid:=NEW.order_id; END IF;
 SELECT * INTO o FROM orders WHERE id=oid;
 SELECT * INTO p FROM pickup_payment_confirmations WHERE order_id=oid;
 IF (o.status='PICKED_UP_BY_CUSTOMER') IS DISTINCT FROM (p.order_id IS NOT NULL) OR
 (p.order_id IS NOT NULL AND (o.fulfillment_type<>'PICKUP' OR o.subtotal_minor<>p.amount_minor OR o.currency<>p.currency OR o.payment_status<>'REPORTED_COLLECTED')) THEN
  RAISE EXCEPTION 'pickup confirmation must match completed order';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER pickup_order_collection AFTER INSERT OR UPDATE ON orders DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_pickup_collection();
CREATE CONSTRAINT TRIGGER pickup_confirmation_collection AFTER INSERT OR UPDATE OR DELETE ON pickup_payment_confirmations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_pickup_collection();
-- A pickup must never acquire distributed delivery work.
CREATE FUNCTION check_delivery_saga() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT FROM orders WHERE id=NEW.order_id AND fulfillment_type='DELIVERY') THEN RAISE EXCEPTION 'pickup cannot have delivery saga'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_saga_only BEFORE INSERT OR UPDATE ON order_sagas FOR EACH ROW EXECUTE FUNCTION check_delivery_saga();
