-- Historical intents are audit records, never a backfill push campaign.
ALTER TABLE notifications DROP CONSTRAINT notifications_status_check;
UPDATE notifications SET status='CANCELLED';
ALTER TABLE notifications ADD CONSTRAINT notifications_status_check CHECK(status IN ('PENDING','PROCESSING','SENT','FAILED','CANCELLED'));
ALTER TABLE notifications DROP CONSTRAINT notifications_event_id_key;
ALTER TABLE notifications DROP CONSTRAINT notifications_order_id_aggregate_version_key;
ALTER TABLE notifications DROP CONSTRAINT notifications_event_id_fkey;
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ALTER COLUMN customer_id DROP NOT NULL;
ALTER TABLE notifications ADD COLUMN recipient_type TEXT NOT NULL DEFAULT 'CUSTOMER' CHECK(recipient_type IN ('CUSTOMER','RESTAURANT','COURIER'));
ALTER TABLE notifications ADD COLUMN recipient_id BIGINT CHECK(recipient_id>0);
UPDATE notifications SET recipient_id=customer_id;
ALTER TABLE notifications ALTER COLUMN recipient_id SET NOT NULL;
ALTER TABLE notifications ADD COLUMN title TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN body TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN data JSONB NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(data)='object');
ALTER TABLE notifications ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE notifications ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE notifications ADD COLUMN offer_id BIGINT;
ALTER TABLE notifications ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD UNIQUE(event_id,recipient_type,recipient_id);
-- Old consumers may still write their historical column set during rollout.
CREATE FUNCTION notification_legacy_recipient() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.recipient_id IS NULL THEN NEW.recipient_id:=NEW.customer_id; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER notification_legacy_recipient BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION notification_legacy_recipient();

CREATE INDEX notifications_processing_idx ON notifications(created_at,id) WHERE status='PROCESSING';
CREATE INDEX notifications_order_active_idx ON notifications(order_id,aggregate_version,id) WHERE status IN ('PENDING','PROCESSING');
CREATE INDEX notifications_offer_active_idx ON notifications(offer_id,id) WHERE offer_id IS NOT NULL AND status IN ('PENDING','PROCESSING');
CREATE TABLE push_events (
 event_id UUID PRIMARY KEY, stream TEXT NOT NULL, aggregate_id BIGINT NOT NULL,
 aggregate_version BIGINT NOT NULL, fingerprint BYTEA NOT NULL,
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(stream,aggregate_id,aggregate_version)
);
CREATE TABLE notification_order_terminals(order_id BIGINT PRIMARY KEY,version BIGINT NOT NULL CHECK(version>0));
CREATE TABLE offer_states (
 offer_id BIGINT PRIMARY KEY, terminal BOOLEAN NOT NULL, expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE device_registrations (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 owner_type TEXT NOT NULL CHECK(owner_type IN ('CUSTOMER','RESTAURANT','COURIER')),
 owner_id BIGINT NOT NULL CHECK(owner_id>0), registered_by BIGINT NOT NULL CHECK(registered_by>0),
 platform TEXT NOT NULL CHECK(platform IN ('IOS','ANDROID','WEB')),
 installation_id TEXT NOT NULL CHECK(length(installation_id) BETWEEN 10 AND 2048),
 target_hash BYTEA NOT NULL, is_active BOOLEAN NOT NULL DEFAULT true,
 last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX device_active_target ON device_registrations(target_hash) WHERE is_active;
CREATE INDEX device_user ON device_registrations(registered_by,id DESC);
CREATE INDEX device_owner ON device_registrations(owner_type,owner_id) WHERE is_active;
CREATE INDEX device_stale ON device_registrations(last_seen_at,id) WHERE is_active;
CREATE TABLE push_deliveries (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 notification_id BIGINT NOT NULL REFERENCES notifications(id), device_id BIGINT NOT NULL REFERENCES device_registrations(id),
 status TEXT NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','PROCESSING','SENT','FAILED','CANCELLED')),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0), next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 lease_until TIMESTAMPTZ, claim_token UUID, last_code TEXT NOT NULL DEFAULT '', provider_message_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(notification_id,device_id), CHECK((status='PROCESSING')=(lease_until IS NOT NULL AND claim_token IS NOT NULL))
);
CREATE INDEX push_due ON push_deliveries(next_attempt_at,id) WHERE status='PENDING';
CREATE INDEX push_recover ON push_deliveries(lease_until,id) WHERE status='PROCESSING';
CREATE TABLE push_attempts (
 delivery_id BIGINT NOT NULL REFERENCES push_deliveries(id), attempt INTEGER NOT NULL,
 started_at TIMESTAMPTZ NOT NULL DEFAULT now(),finished_at TIMESTAMPTZ,
 code TEXT NOT NULL DEFAULT 'UNKNOWN', provider_message_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(delivery_id,attempt)
);
-- A service-wide PostgreSQL limiter bounds total send starts across replicas.
CREATE TABLE push_rate_limit (id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),next_at TIMESTAMPTZ NOT NULL DEFAULT now());
INSERT INTO push_rate_limit DEFAULT VALUES;
