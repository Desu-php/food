-- Existing restaurants require explicit delivery activation and configured tariffs.
ALTER TABLE restaurants ADD COLUMN delivery_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE restaurants ADD COLUMN pickup_enabled BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE restaurants ADD COLUMN minimum_delivery_subtotal_minor BIGINT NOT NULL DEFAULT 0 CHECK(minimum_delivery_subtotal_minor>=0);
ALTER TABLE restaurants ADD COLUMN pricing_version BIGINT NOT NULL DEFAULT 1 CHECK(pricing_version>0);
CREATE TABLE delivery_tariffs (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 restaurant_id BIGINT NOT NULL REFERENCES restaurants(id),
 max_distance_meters BIGINT NOT NULL CHECK(max_distance_meters>0),
 delivery_fee_minor BIGINT NOT NULL CHECK(delivery_fee_minor>=0),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(restaurant_id,max_distance_meters)
);
-- Also version location/availability changes affecting quotes.
CREATE FUNCTION bump_restaurant_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.pricing_version := OLD.pricing_version+1;
 RETURN NEW;
END $$;
CREATE TRIGGER restaurant_pricing_version BEFORE UPDATE ON restaurants FOR EACH ROW EXECUTE FUNCTION bump_restaurant_pricing();
