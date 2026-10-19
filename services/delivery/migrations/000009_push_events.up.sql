ALTER TABLE outbox_events ADD COLUMN stream TEXT NOT NULL DEFAULT 'delivery' CHECK(stream IN ('delivery','courier_offers'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_aggregate_id_aggregate_version_key;
ALTER TABLE outbox_events ADD UNIQUE(stream,aggregate_id,aggregate_version);
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK((stream='courier_offers' AND event_type IN ('CourierOfferCreated','CourierOfferClosed')) OR (stream='delivery' AND event_type IN ('DeliveryCreated','CourierAssigned','DeliveryPickedUp','DeliveryCompleted','DeliveryCancelled','CourierReleased','DeliveryCancellationRejected','DeliveryStateReported')));
