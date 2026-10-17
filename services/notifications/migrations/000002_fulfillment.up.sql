ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK(kind IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderOnTheWay','OrderDelivered'));
