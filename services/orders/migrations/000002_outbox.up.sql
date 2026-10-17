ALTER TABLE orders ADD COLUMN version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0);
UPDATE orders SET version=2 WHERE status='CANCELLED';

CREATE TABLE outbox_events (
 event_id UUID PRIMARY KEY,
 aggregate_id BIGINT NOT NULL REFERENCES orders(id),
 aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
 event_type TEXT NOT NULL CHECK (event_type IN ('OrderCreated','OrderCancelled')),
 correlation_id TEXT NOT NULL,
 body JSONB NOT NULL CHECK (jsonb_typeof(body)='object'),
 status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','PROCESSING','PUBLISHED','DEAD')),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 claim_token UUID,
 lease_until TIMESTAMPTZ,
 last_error TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 published_at TIMESTAMPTZ,
 UNIQUE (aggregate_id,aggregate_version),
 CHECK ((status='PROCESSING') = (claim_token IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK ((status='PUBLISHED') = (published_at IS NOT NULL))
);
CREATE INDEX outbox_pending_idx ON outbox_events(available_at,created_at) WHERE status='PENDING';
CREATE INDEX outbox_leases_idx ON outbox_events(lease_until) WHERE status='PROCESSING';
CREATE INDEX outbox_unpublished_aggregate_idx ON outbox_events(aggregate_id,aggregate_version) WHERE status <> 'PUBLISHED';
CREATE INDEX outbox_retention_idx ON outbox_events(published_at) WHERE status='PUBLISHED';

-- No historical backfill: pre-migration orders did not promise integration events.
