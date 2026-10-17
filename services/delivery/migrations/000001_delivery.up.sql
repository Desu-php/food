CREATE TABLE couriers (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name TEXT NOT NULL CHECK(char_length(btrim(name)) BETWEEN 1 AND 200),
 status TEXT NOT NULL DEFAULT 'AVAILABLE' CHECK(status IN ('AVAILABLE','BUSY')),
 created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE deliveries (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 order_id BIGINT NOT NULL UNIQUE CHECK(order_id>0),
 courier_id BIGINT REFERENCES couriers(id),
 status TEXT NOT NULL CHECK(status IN ('PENDING_ASSIGNMENT','ASSIGNED','PICKED_UP','DELIVERED','CANCELLED')),
 amount_to_collect_minor BIGINT NOT NULL CHECK(amount_to_collect_minor>0),
 currency TEXT NOT NULL CHECK(currency='TJS'),
 version BIGINT NOT NULL CHECK(version>0),
 order_version BIGINT NOT NULL CHECK(order_version>0),
 ready BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL CHECK(updated_at>=created_at),
 CHECK(status NOT IN ('ASSIGNED','PICKED_UP','DELIVERED') OR courier_id IS NOT NULL),
 CHECK(status<>'PENDING_ASSIGNMENT' OR courier_id IS NULL)
);
CREATE UNIQUE INDEX courier_active_delivery_idx ON deliveries(courier_id) WHERE status IN ('ASSIGNED','PICKED_UP');
CREATE INDEX deliveries_status_idx ON deliveries(status,id);
CREATE TABLE pos_confirmations (
 delivery_id BIGINT PRIMARY KEY REFERENCES deliveries(id),
 order_id BIGINT NOT NULL UNIQUE CHECK(order_id>0),
 amount_minor BIGINT NOT NULL CHECK(amount_minor>0),
 currency TEXT NOT NULL CHECK(currency='TJS'),
 payment_method TEXT NOT NULL CHECK(payment_method='POS_ON_DELIVERY'),
 pos_reference TEXT NOT NULL CHECK(char_length(pos_reference)<=200),
 confirmed_by BIGINT NOT NULL REFERENCES couriers(id),
 confirmed_at TIMESTAMPTZ NOT NULL
);
-- Cross-table consistency is checked at transaction commit, allowing atomic writes in either order.
CREATE FUNCTION validate_delivery_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id BIGINT; d deliveries%ROWTYPE; p pos_confirmations%ROWTYPE;
BEGIN
 IF TG_TABLE_NAME='deliveries' THEN target_id:=NEW.id; ELSE target_id:=NEW.delivery_id; END IF;
 SELECT * INTO d FROM deliveries WHERE id=target_id;
 SELECT * INTO p FROM pos_confirmations WHERE delivery_id=target_id;
 IF (d.status='DELIVERED') <> (p.delivery_id IS NOT NULL) OR
 (p.delivery_id IS NOT NULL AND (p.order_id<>d.order_id OR p.amount_minor<>d.amount_to_collect_minor OR p.currency<>d.currency OR p.confirmed_by<>d.courier_id OR p.confirmed_at<>d.updated_at)) THEN
 RAISE EXCEPTION 'delivery and operational POS confirmation must agree';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER delivery_confirmation_check AFTER INSERT OR UPDATE ON deliveries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_delivery_confirmation();
CREATE CONSTRAINT TRIGGER confirmation_delivery_check AFTER INSERT OR UPDATE ON pos_confirmations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_delivery_confirmation();
CREATE TABLE order_streams(order_id BIGINT PRIMARY KEY, version BIGINT NOT NULL DEFAULT 0 CHECK(version>=0));
CREATE TABLE processed_events (
 event_id UUID PRIMARY KEY,
 order_id BIGINT NOT NULL CHECK(order_id>0),
 aggregate_version BIGINT NOT NULL CHECK(aggregate_version>0),
 fingerprint BYTEA NOT NULL,
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(order_id,aggregate_version)
);

CREATE TABLE outbox_events (
 event_id UUID PRIMARY KEY,
 aggregate_id BIGINT NOT NULL REFERENCES deliveries(id),
 aggregate_version BIGINT NOT NULL CHECK (aggregate_version > 0),
 event_type TEXT NOT NULL CHECK (event_type IN ('DeliveryCreated','CourierAssigned','DeliveryPickedUp','DeliveryCompleted','DeliveryCancelled')),
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

