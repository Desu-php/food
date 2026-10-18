-- Refuse a destructive rollback after new pricing snapshots have been used.
DO $$ BEGIN IF EXISTS(SELECT FROM orders WHERE delivery_fee_minor<>0 OR pricing_snapshot IS NOT NULL OR delivery_latitude IS NOT NULL) THEN RAISE EXCEPTION 'cannot downgrade orders with pricing snapshots'; END IF; END $$;
DROP TRIGGER order_pricing_immutable ON orders;
DROP FUNCTION preserve_order_pricing();
ALTER TABLE orders DROP CONSTRAINT orders_delivery_coordinates_check, DROP COLUMN total_minor, DROP COLUMN delivery_latitude, DROP COLUMN delivery_longitude, DROP COLUMN pricing_snapshot;
ALTER TABLE orders DROP CONSTRAINT orders_delivery_fee_minor_check;
ALTER TABLE orders ADD CONSTRAINT orders_delivery_fee_minor_check CHECK(delivery_fee_minor=0);
