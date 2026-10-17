ALTER TABLE couriers ADD COLUMN updated_at TIMESTAMPTZ;
UPDATE couriers SET updated_at=created_at;
ALTER TABLE couriers ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE couriers ADD CHECK(updated_at>=created_at);
ALTER TABLE couriers DROP CONSTRAINT couriers_status_check;
ALTER TABLE couriers ADD CONSTRAINT couriers_status_check CHECK(status IN ('OFFLINE','AVAILABLE','BUSY'));
ALTER TABLE deliveries ADD COLUMN preparation_started BOOLEAN NOT NULL DEFAULT false;
UPDATE deliveries SET preparation_started=true WHERE ready OR status IN ('PICKED_UP','DELIVERED');
ALTER TABLE pos_confirmations RENAME TO payment_confirmations;
ALTER TABLE payment_confirmations RENAME COLUMN pos_reference TO receipt_reference;
ALTER TABLE payment_confirmations ADD COLUMN id BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE;
CREATE OR REPLACE FUNCTION validate_delivery_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id BIGINT; d deliveries%ROWTYPE; p payment_confirmations%ROWTYPE;
BEGIN
 IF TG_TABLE_NAME='deliveries' THEN target_id:=NEW.id; ELSE target_id:=NEW.delivery_id; END IF;
 SELECT * INTO d FROM deliveries WHERE id=target_id;
 SELECT * INTO p FROM payment_confirmations WHERE delivery_id=target_id;
 IF (d.status='DELIVERED') <> (p.delivery_id IS NOT NULL) OR
 (p.delivery_id IS NOT NULL AND (p.order_id<>d.order_id OR p.amount_minor<>d.amount_to_collect_minor OR p.currency<>d.currency OR p.confirmed_by<>d.courier_id OR p.confirmed_at>d.updated_at)) THEN
 RAISE EXCEPTION 'delivery and operational POS confirmation must agree';
 END IF;
 RETURN NULL;
END $$;
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('DeliveryCreated','CourierAssigned','DeliveryPickedUp','DeliveryCompleted','DeliveryCancelled','CourierReleased','DeliveryCancellationRejected','DeliveryStateReported'));
CREATE TABLE delivery_requests (
 order_id BIGINT PRIMARY KEY CHECK(order_id>0),
 command_version BIGINT NOT NULL DEFAULT 0 CHECK(command_version>=0),
 cancelled BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE command_inbox (
 message_id UUID PRIMARY KEY,
 order_id BIGINT NOT NULL CHECK(order_id>0),
 command_version BIGINT NOT NULL CHECK(command_version>0),
 fingerprint BYTEA NOT NULL,
 response_event_id UUID,
 processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(order_id,command_version)
);
CREATE INDEX couriers_available_idx ON couriers(id) WHERE status='AVAILABLE';
ALTER TABLE order_streams ADD COLUMN preparation_started BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE order_streams ADD COLUMN ready BOOLEAN NOT NULL DEFAULT false;
UPDATE order_streams s SET preparation_started=d.preparation_started,ready=d.ready FROM deliveries d WHERE d.order_id=s.order_id;
