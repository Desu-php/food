ALTER TABLE orders DROP CONSTRAINT orders_delivery_fee_minor_check;
ALTER TABLE orders ADD CONSTRAINT orders_delivery_fee_minor_check CHECK(delivery_fee_minor>=0 AND (fulfillment_type='DELIVERY' OR delivery_fee_minor=0));
-- Generated from historical agreed subtotal + historic zero fee; no live repricing.
ALTER TABLE orders ADD COLUMN total_minor BIGINT GENERATED ALWAYS AS (subtotal_minor+delivery_fee_minor) STORED;
ALTER TABLE orders ADD COLUMN delivery_latitude DOUBLE PRECISION, ADD COLUMN delivery_longitude DOUBLE PRECISION, ADD COLUMN pricing_snapshot JSONB;
ALTER TABLE orders ADD CONSTRAINT orders_delivery_coordinates_check CHECK (
 (delivery_latitude IS NULL AND delivery_longitude IS NULL) OR
 (fulfillment_type='DELIVERY' AND delivery_latitude IS NOT NULL AND delivery_longitude IS NOT NULL AND delivery_latitude BETWEEN -90 AND 90 AND delivery_longitude BETWEEN -180 AND 180));
CREATE FUNCTION preserve_order_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.subtotal_minor,NEW.currency,NEW.delivery_latitude,NEW.delivery_longitude,NEW.latitude,NEW.longitude,NEW.pricing_snapshot)
 IS DISTINCT FROM (OLD.subtotal_minor,OLD.currency,OLD.delivery_latitude,OLD.delivery_longitude,OLD.latitude,OLD.longitude,OLD.pricing_snapshot) THEN
 RAISE EXCEPTION 'order pricing and locations are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER order_pricing_immutable BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION preserve_order_pricing();
