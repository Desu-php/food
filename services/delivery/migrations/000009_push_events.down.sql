DO $$ BEGIN IF EXISTS(SELECT FROM outbox_events WHERE stream='courier_offers') THEN RAISE EXCEPTION 'offer event audit exists; downgrade refused'; END IF; END $$;
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(event_type IN ('DeliveryCreated','CourierAssigned','DeliveryPickedUp','DeliveryCompleted','DeliveryCancelled','CourierReleased','DeliveryCancellationRejected','DeliveryStateReported'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_stream_aggregate_id_aggregate_version_key;
ALTER TABLE outbox_events ADD UNIQUE(aggregate_id,aggregate_version);
ALTER TABLE outbox_events DROP COLUMN stream;
