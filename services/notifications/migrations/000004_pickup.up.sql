ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK(kind IN ('OrderCreated','OrderCancelled','OrderAccepted','OrderRejected','OrderPreparing','OrderReadyForPickup','OrderPickedUpByCustomer','OrderOnTheWay','OrderDelivered','OrderWaitingCourier','OrderCourierAssigned','OrderCourierReleased','OrderCancellationRequested','OrderResumed'));
ALTER TABLE notifications ADD COLUMN fulfillment_type TEXT CHECK(fulfillment_type IN ('DELIVERY','PICKUP'));
