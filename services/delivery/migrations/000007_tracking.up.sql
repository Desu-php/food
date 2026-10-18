-- Unknown historical ownership stays NULL; only trusted commands can bind it.
ALTER TABLE delivery_requests ADD COLUMN customer_id BIGINT CHECK(customer_id > 0);
