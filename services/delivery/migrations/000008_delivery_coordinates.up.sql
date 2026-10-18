ALTER TABLE deliveries ADD COLUMN delivery_latitude DOUBLE PRECISION, ADD COLUMN delivery_longitude DOUBLE PRECISION;
ALTER TABLE deliveries ADD CONSTRAINT deliveries_destination_check CHECK(
 (delivery_latitude IS NULL AND delivery_longitude IS NULL) OR
 (delivery_latitude IS NOT NULL AND delivery_longitude IS NOT NULL AND delivery_latitude BETWEEN -90 AND 90 AND delivery_longitude BETWEEN -180 AND 180));
CREATE FUNCTION preserve_delivery_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.amount_to_collect_minor,NEW.currency,NEW.delivery_address,NEW.latitude,NEW.longitude,NEW.delivery_latitude,NEW.delivery_longitude)
 IS DISTINCT FROM (OLD.amount_to_collect_minor,OLD.currency,OLD.delivery_address,OLD.latitude,OLD.longitude,OLD.delivery_latitude,OLD.delivery_longitude) THEN
 RAISE EXCEPTION 'delivery collection amount and locations are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_pricing_immutable BEFORE UPDATE ON deliveries FOR EACH ROW EXECUTE FUNCTION preserve_delivery_pricing();
