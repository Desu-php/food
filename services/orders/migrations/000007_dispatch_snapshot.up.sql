ALTER TABLE orders ADD COLUMN latitude DOUBLE PRECISION, ADD COLUMN longitude DOUBLE PRECISION;
ALTER TABLE orders ADD CONSTRAINT orders_coordinates_check CHECK (
 (latitude IS NULL AND longitude IS NULL) OR
 (latitude IS NOT NULL AND longitude IS NOT NULL AND latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180));
ALTER TABLE order_sagas ADD COLUMN dispatch_deadline TIMESTAMPTZ;
