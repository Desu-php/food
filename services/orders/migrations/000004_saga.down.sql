DO $$ BEGIN
 IF EXISTS(SELECT FROM order_sagas WHERE command_sequence>0) OR
 EXISTS(SELECT FROM outbox_events WHERE stream='delivery_commands' OR event_type IN ('OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed')) OR
 EXISTS(SELECT FROM orders WHERE status='WAITING_COURIER' OR cancellation_requested OR courier_id<>0) THEN
 RAISE EXCEPTION 'saga fulfillment data exists; roll forward or restore a consistent backup';
 END IF;
END $$;
DROP TABLE order_sagas;
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_stream_aggregate_id_aggregate_version_key;
ALTER TABLE outbox_events ADD UNIQUE(aggregate_id,aggregate_version);
ALTER TABLE outbox_events DROP COLUMN stream;
ALTER TABLE orders DROP COLUMN courier_id,DROP COLUMN cancellation_requested;
ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('WAITING_RESTAURANT','ACCEPTED','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','DELIVERED','CANCELLED'));
