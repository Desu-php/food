CREATE TABLE orders (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 customer_id BIGINT NOT NULL CHECK (customer_id > 0),
 restaurant_id BIGINT NOT NULL CHECK (restaurant_id > 0),
 status TEXT NOT NULL CHECK (status IN ('PENDING_PAYMENT', 'CANCELLED')),
 currency TEXT NOT NULL CHECK (currency = 'TJS'),
 subtotal_minor BIGINT NOT NULL CHECK (subtotal_minor > 0),
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL CHECK (updated_at >= created_at)
);
CREATE INDEX orders_customer_created_idx ON orders (customer_id, created_at DESC, id DESC);
CREATE TABLE order_items (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 order_id BIGINT NOT NULL REFERENCES orders(id),
 menu_item_id BIGINT NOT NULL CHECK (menu_item_id > 0),
 name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200 AND char_length(name) <= 200),
 quantity BIGINT NOT NULL CHECK (quantity BETWEEN 1 AND 100),
 unit_price_minor BIGINT NOT NULL CHECK (unit_price_minor > 0),
 total_price_minor BIGINT NOT NULL CHECK (total_price_minor > 0 AND total_price_minor::numeric = unit_price_minor::numeric * quantity),
 UNIQUE (order_id, menu_item_id)
);
CREATE TABLE idempotency_keys (
 customer_id BIGINT NOT NULL CHECK (customer_id > 0),
 key TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 128 AND key ~ '^[!-~]+$'),
 request_hash TEXT NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 order_id BIGINT NOT NULL UNIQUE REFERENCES orders(id),
 response JSONB NOT NULL CHECK (jsonb_typeof(response) = 'object'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (customer_id, key)
);
