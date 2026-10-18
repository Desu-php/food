DROP TRIGGER delivery_pricing_immutable ON deliveries;
DROP FUNCTION preserve_delivery_pricing();
ALTER TABLE deliveries DROP CONSTRAINT deliveries_destination_check, DROP COLUMN delivery_latitude, DROP COLUMN delivery_longitude;
