ALTER TABLE order_streams DROP COLUMN dispatch_blocked;
DROP TABLE dispatch_declines;
DROP INDEX deliveries_dispatch_due_idx;
ALTER TABLE deliveries DROP COLUMN dispatch_deadline, DROP COLUMN dispatch_paused, DROP COLUMN dispatch_attempts, DROP COLUMN dispatch_next_at, DROP COLUMN dispatch_token, DROP COLUMN dispatch_lease_until, DROP COLUMN dispatch_reason;
ALTER TABLE deliveries DROP CONSTRAINT deliveries_coordinates_check, DROP COLUMN latitude, DROP COLUMN longitude;
