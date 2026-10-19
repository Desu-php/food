ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_stream_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_stream_check CHECK(stream IN ('orders','delivery_commands','fulfillment'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK((stream='fulfillment' AND event_type='OrderNeedsAttention') OR
 (stream='delivery_commands' AND event_type IN ('CreateDelivery','CancelDelivery')) OR
 (stream='orders' AND event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderPickedUpByCustomer','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed')));
