ALTER TABLE orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check CHECK(status IN ('WAITING_RESTAURANT','ACCEPTED','WAITING_COURIER','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','DELIVERED','CANCELLED'));
ALTER TABLE orders ADD COLUMN courier_id BIGINT NOT NULL DEFAULT 0 CHECK(courier_id>=0);
ALTER TABLE orders ADD COLUMN cancellation_requested BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE outbox_events ADD COLUMN stream TEXT NOT NULL DEFAULT 'orders' CHECK(stream IN ('orders','delivery_commands'));
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_aggregate_id_aggregate_version_key;
ALTER TABLE outbox_events ADD UNIQUE(stream,aggregate_id,aggregate_version);
ALTER TABLE outbox_events DROP CONSTRAINT outbox_events_event_type_check;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_events_event_type_check CHECK(
 (stream='delivery_commands' AND event_type IN ('CreateDelivery','CancelDelivery')) OR
 (stream='orders' AND event_type IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed')));
CREATE TABLE order_sagas (
 order_id BIGINT PRIMARY KEY REFERENCES orders(id),
 state TEXT NOT NULL CHECK(state IN ('WAITING_RESTAURANT','CREATING_DELIVERY','WAITING_COURIER','COURIER_ASSIGNED','PREPARING','READY_FOR_PICKUP','ON_THE_WAY','CANCELLING','COMPLETED','CANCELLED','NEEDS_ATTENTION')),
 version BIGINT NOT NULL CHECK(version>0),
 delivery_id BIGINT NOT NULL DEFAULT 0 CHECK(delivery_id>=0),
 delivery_version BIGINT NOT NULL DEFAULT 0 CHECK(delivery_version>=0),
 courier_id BIGINT NOT NULL DEFAULT 0 CHECK(courier_id>=0),
 command_id TEXT NOT NULL DEFAULT '',
 command_type TEXT NOT NULL DEFAULT '' CHECK(command_type IN ('','CreateDelivery','CancelDelivery')),
 command_sequence BIGINT NOT NULL DEFAULT 0 CHECK(command_sequence>=0),
 attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0),
 deadline TIMESTAMPTZ NOT NULL,
 next_attempt_at TIMESTAMPTZ NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 cancel_reason TEXT NOT NULL DEFAULT '',
 legacy_reconciliation BOOLEAN NOT NULL DEFAULT false,
 correlation_id TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL CHECK(updated_at>=created_at)
);
CREATE INDEX sagas_due_idx ON order_sagas(next_attempt_at,order_id) WHERE state NOT IN ('WAITING_RESTAURANT','COMPLETED','CANCELLED','NEEDS_ATTENTION');
CREATE INDEX sagas_attention_idx ON order_sagas(order_id) WHERE state='NEEDS_ATTENTION';
-- Existing active orders are reconciled through CreateDelivery/state reports, never cross-service SQL.
-- Historical versions, Outbox bytes and creation responses remain unchanged.
INSERT INTO order_sagas(order_id,state,version,delivery_id,delivery_version,deadline,next_attempt_at,reason,created_at,updated_at)
SELECT o.id,CASE WHEN o.status='WAITING_RESTAURANT' THEN 'WAITING_RESTAURANT' WHEN o.status='DELIVERED' THEN 'COMPLETED' WHEN o.status='CANCELLED' THEN 'CANCELLED' ELSE 'CREATING_DELIVERY' END,
1,COALESCE(d.delivery_id,0),COALESCE(d.version,0),now(),now(),CASE WHEN o.status IN ('ACCEPTED','PREPARING','READY_FOR_PICKUP','ON_THE_WAY') THEN 'legacy_reconciliation' ELSE '' END,o.created_at,GREATEST(o.updated_at,now())
FROM orders o LEFT JOIN order_deliveries d ON d.order_id=o.id;

UPDATE order_sagas SET legacy_reconciliation=true WHERE reason='legacy_reconciliation';
