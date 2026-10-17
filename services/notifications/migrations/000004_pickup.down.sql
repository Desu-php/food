DO $$ BEGIN
 IF EXISTS(SELECT FROM notifications WHERE kind='OrderPickedUpByCustomer' OR fulfillment_type='PICKUP') THEN RAISE EXCEPTION 'cannot discard pickup notification history; roll forward'; END IF;
END $$;
ALTER TABLE notifications DROP COLUMN fulfillment_type;
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK(kind IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed'));
