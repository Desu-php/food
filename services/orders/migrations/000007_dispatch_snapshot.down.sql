ALTER TABLE order_sagas DROP COLUMN dispatch_deadline;
ALTER TABLE orders DROP CONSTRAINT orders_coordinates_check, DROP COLUMN latitude, DROP COLUMN longitude;
