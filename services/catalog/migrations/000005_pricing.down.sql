DROP TRIGGER restaurant_pricing_version ON restaurants;
DROP FUNCTION bump_restaurant_pricing();
DROP TABLE delivery_tariffs;
ALTER TABLE restaurants DROP COLUMN delivery_enabled, DROP COLUMN pickup_enabled, DROP COLUMN minimum_delivery_subtotal_minor, DROP COLUMN pricing_version;
