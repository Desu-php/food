ALTER TABLE deliveries ADD COLUMN latitude DOUBLE PRECISION, ADD COLUMN longitude DOUBLE PRECISION;
ALTER TABLE deliveries ADD CONSTRAINT deliveries_coordinates_check CHECK (
 (latitude IS NULL AND longitude IS NULL) OR
 (latitude IS NOT NULL AND longitude IS NOT NULL AND latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180));
ALTER TABLE deliveries
 ADD COLUMN dispatch_deadline TIMESTAMPTZ,
 ADD COLUMN dispatch_paused BOOLEAN NOT NULL DEFAULT false,
 ADD COLUMN dispatch_attempts INTEGER NOT NULL DEFAULT 0 CHECK(dispatch_attempts>=0),
 ADD COLUMN dispatch_next_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 ADD COLUMN dispatch_token UUID,
 ADD COLUMN dispatch_lease_until TIMESTAMPTZ,
 ADD COLUMN dispatch_reason TEXT NOT NULL DEFAULT '',
 ADD CHECK((dispatch_token IS NULL) = (dispatch_lease_until IS NULL));
CREATE INDEX deliveries_dispatch_due_idx ON deliveries(dispatch_next_at,id) WHERE status='PENDING_ASSIGNMENT' AND NOT preparation_started AND NOT dispatch_paused;
CREATE TABLE dispatch_declines (
 delivery_id BIGINT NOT NULL REFERENCES deliveries(id),
 courier_id BIGINT NOT NULL REFERENCES couriers(id),
 declined_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(delivery_id,courier_id)
);
ALTER TABLE order_streams ADD COLUMN dispatch_blocked BOOLEAN NOT NULL DEFAULT false;
