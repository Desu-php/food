CREATE TABLE courier_offers (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 delivery_id BIGINT NOT NULL REFERENCES deliveries(id),
 courier_id BIGINT NOT NULL REFERENCES couriers(id),
 status TEXT NOT NULL CHECK(status IN ('PENDING','ACCEPTED','DECLINED','EXPIRED','CANCELLED')),
 expires_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 responded_at TIMESTAMPTZ,
 CHECK(expires_at > created_at AND expires_at <= created_at + interval '30 seconds'),
 CHECK((status='PENDING') = (responded_at IS NULL)),
 CHECK(responded_at IS NULL OR responded_at >= created_at)
);
-- Expiry must be persisted before a reservation can be reused; clock predicates
-- are deliberately absent from the unique indexes.
CREATE UNIQUE INDEX courier_offers_pending_delivery_idx ON courier_offers(delivery_id) WHERE status='PENDING';
CREATE UNIQUE INDEX courier_offers_pending_courier_idx ON courier_offers(courier_id) WHERE status='PENDING';
CREATE INDEX courier_offers_expiry_idx ON courier_offers(expires_at,delivery_id) WHERE status='PENDING';
