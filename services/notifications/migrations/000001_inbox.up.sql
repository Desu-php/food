CREATE TABLE processed_events (
 event_id UUID PRIMARY KEY,
 order_id BIGINT NOT NULL CHECK (order_id > 0),
 aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
 fingerprint BYTEA NOT NULL,
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (order_id,aggregate_version)
);

CREATE TABLE notifications (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id UUID NOT NULL UNIQUE REFERENCES processed_events(event_id),
 order_id BIGINT NOT NULL CHECK (order_id > 0),
 aggregate_version BIGINT NOT NULL,
 customer_id BIGINT NOT NULL CHECK (customer_id > 0),
 kind TEXT NOT NULL CHECK (kind IN ('OrderCreated','OrderCancelled')),
 status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status='PENDING'),
 correlation_id TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE (order_id,aggregate_version)
);
CREATE INDEX notifications_pending_idx ON notifications(created_at,id) WHERE status='PENDING';

-- Durable quarantine replaces a DLQ topic for this minimal service. Offsets may
-- advance only after this record is committed. Preserve the original bytes.
CREATE TABLE failed_messages (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 topic TEXT NOT NULL,
 partition INTEGER NOT NULL,
 offset_value BIGINT NOT NULL,
 message_key BYTEA NOT NULL,
 body BYTEA NOT NULL,
 reason TEXT NOT NULL,
 attempts INTEGER NOT NULL CHECK (attempts > 0),
 status TEXT NOT NULL DEFAULT 'QUARANTINED' CHECK (status IN ('QUARANTINED','RESOLVED')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 resolved_at TIMESTAMPTZ,
 UNIQUE (topic,partition,offset_value),
 CHECK ((status='RESOLVED') = (resolved_at IS NOT NULL))
);
CREATE INDEX failed_messages_unresolved_idx ON failed_messages(created_at,id) WHERE status='QUARANTINED';
