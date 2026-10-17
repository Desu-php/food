DO $$ BEGIN
 IF EXISTS(SELECT FROM command_inbox) OR EXISTS(SELECT FROM couriers WHERE status='OFFLINE') OR
 EXISTS(SELECT FROM outbox_events WHERE event_type IN ('CourierReleased','DeliveryCancellationRejected','DeliveryStateReported')) THEN
 RAISE EXCEPTION 'saga delivery data exists; roll forward or restore a consistent backup';
 END IF;
END $$;
ALTER TABLE order_streams DROP COLUMN preparation_started,DROP COLUMN ready;
DROP TABLE command_inbox;
DROP TABLE delivery_requests;
DROP INDEX couriers_available_idx;
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('DeliveryCreated','CourierAssigned','DeliveryPickedUp','DeliveryCompleted','DeliveryCancelled'));
ALTER TABLE deliveries DROP COLUMN preparation_started;
ALTER TABLE couriers DROP COLUMN updated_at;
ALTER TABLE couriers DROP CONSTRAINT couriers_status_check;
ALTER TABLE couriers ADD CONSTRAINT couriers_status_check CHECK(status IN ('AVAILABLE','BUSY'));
ALTER TABLE payment_confirmations DROP COLUMN id;
ALTER TABLE payment_confirmations RENAME COLUMN receipt_reference TO pos_reference;
ALTER TABLE payment_confirmations RENAME TO pos_confirmations;
CREATE OR REPLACE FUNCTION validate_delivery_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id BIGINT; d deliveries%ROWTYPE; p pos_confirmations%ROWTYPE;
BEGIN
 IF TG_TABLE_NAME='deliveries' THEN target_id:=NEW.id; ELSE target_id:=NEW.delivery_id; END IF;
 SELECT * INTO d FROM deliveries WHERE id=target_id;
 SELECT * INTO p FROM pos_confirmations WHERE delivery_id=target_id;
 IF (d.status='DELIVERED') <> (p.delivery_id IS NOT NULL) OR
 (p.delivery_id IS NOT NULL AND (p.order_id<>d.order_id OR p.amount_minor<>d.amount_to_collect_minor OR p.currency<>d.currency OR p.confirmed_by<>d.courier_id OR p.confirmed_at>d.updated_at)) THEN RAISE EXCEPTION 'delivery and POS confirmation must agree'; END IF;
 RETURN NULL;
END $$;
