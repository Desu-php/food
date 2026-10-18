package application

import (
	"context"
	"errors"
	"testing"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func fulfillmentOrder(t *testing.T) (domain.Order, domain.Saga, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	o, err := domain.NewOrder(7, 1, "TJS", []domain.PricedItem{{MenuItemID: 1, RestaurantID: 1, Name: "Dish", Quantity: 1, UnitPriceMinor: 25000, Currency: "TJS"}}, now, domain.Fulfillment{Type: domain.Delivery, DeliveryAddress: "Test delivery address"})
	if err != nil {
		t.Fatal(err)
	}
	state := o.Snapshot()
	state.ID = 42
	state.Items[0].ID = 1
	state.Items[0].OrderID = 42
	o, err = domain.Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	return o, domain.NewSaga(42, now), now
}
func deliveryFact(kind string, version int64, now time.Time) delivery.Event {
	courier := int64(5)
	if kind == delivery.DeliveryCreated || kind == delivery.CourierReleased {
		courier = 0
	}
	return delivery.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: 99, AggregateVersion: version, CorrelationID: "saga-test", OccurredAt: now, Payload: delivery.Payload{OrderID: 42, CourierID: courier, AmountToCollectMinor: 25000, Currency: "TJS"}}
}
func advanceSaga(t *testing.T, f *Fulfillment, o *domain.Order, s *domain.Saga, kind string, version int64, now time.Time) {
	t.Helper()
	s.DeliveryID = 99
	s.DeliveryVersion = version
	if _, err := f.HandleDelivery(context.Background(), o, s, deliveryFact(kind, version, now), now); err != nil {
		t.Fatal(kind, err)
	}
}
func TestSagaSuccessAndPreparationGate(t *testing.T) {
	o, s, now := fulfillmentOrder(t)
	f := NewFulfillment(DefaultSagaConfig())
	ctx := context.Background()
	change, err := f.Accept(ctx, &o, &s, now)
	if err != nil || len(change.Commands) != 1 || change.Commands[0].CommandType != commands.CreateDelivery || s.State != domain.SagaCreatingDelivery {
		t.Fatal(change, s, err)
	}
	if _, err = f.Prepare(&o, &s, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal("prepared before delivery", err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryCreated, 1, now)
	if o.Snapshot().Status != domain.WaitingCourier {
		t.Fatal(o.Snapshot())
	}
	if _, err = f.Prepare(&o, &s, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatal("prepared without courier", err)
	}
	advanceSaga(t, f, &o, &s, delivery.CourierAssigned, 2, now)
	if _, err = f.Prepare(&o, &s, now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Ready(&o, &s, now); err != nil {
		t.Fatal(err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryPickedUp, 3, now)
	e := deliveryFact(delivery.DeliveryCompleted, 4, now)
	e.Payload.Confirmation = &delivery.Confirmation{OrderID: 42, DeliveryID: 99, AmountMinor: 25000, Currency: "TJS", PaymentMethod: "POS_ON_DELIVERY", ConfirmedBy: 5, ConfirmedAt: now}
	if _, err = f.HandleDelivery(ctx, &o, &s, e, now); err != nil {
		t.Fatal(err)
	}
	if s.State != domain.SagaCompleted || o.Snapshot().Status != domain.Delivered || o.Snapshot().PaymentStatus != domain.ReportedCollected {
		t.Fatal(o.Snapshot(), s)
	}
	before := o.Snapshot().Version
	if _, err = f.HandleDelivery(ctx, &o, &s, e, now); err != nil || o.Snapshot().Version != before {
		t.Fatal("terminal saga mutated", err)
	}
}
func TestAssignmentTimeoutCompensatesOnlyBeforeCooking(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[prepared], func(t *testing.T) {
			o, s, now := fulfillmentOrder(t)
			cfg := DefaultSagaConfig()
			cfg.CancelBeforePreparing = true
			f := NewFulfillment(cfg)
			ctx := context.Background()
			if _, err := f.Accept(ctx, &o, &s, now); err != nil {
				t.Fatal(err)
			}
			advanceSaga(t, f, &o, &s, delivery.DeliveryCreated, 1, now)
			if prepared {
				advanceSaga(t, f, &o, &s, delivery.CourierAssigned, 2, now)
				if _, err := f.Prepare(&o, &s, now); err != nil {
					t.Fatal(err)
				}
				advanceSaga(t, f, &o, &s, delivery.CourierReleased, 3, now)
			}
			change, err := f.Tick(ctx, &o, &s, s.Deadline.Add(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			if prepared {
				if s.State != domain.SagaNeedsAttention || len(change.Commands) != 0 || o.Snapshot().Status != domain.Preparing || o.Snapshot().CancellationRequested {
					t.Fatal("prepared food auto-cancelled", s, change)
				}
			} else {
				if s.State != domain.SagaCancelling || !o.Snapshot().CancellationRequested || o.Snapshot().Status == domain.Cancelled || len(change.Commands) != 1 || change.Commands[0].CommandType != commands.CancelDelivery {
					t.Fatal(s, change)
				}
				advanceSaga(t, f, &o, &s, delivery.CourierAssigned, 2, now)
				if _, err = f.Prepare(&o, &s, now); err == nil {
					t.Fatal("prepare raced cancellation")
				}
				advanceSaga(t, f, &o, &s, delivery.DeliveryCancelled, 3, now)
				if s.State != domain.SagaCancelled || o.Snapshot().Status != domain.Cancelled {
					t.Fatal(s, o.Snapshot())
				}
			}
		})
	}
}
func TestSagaRetryRecoveryAndEscalation(t *testing.T) {
	o, s, now := fulfillmentOrder(t)
	cfg := DefaultSagaConfig()
	cfg.MaxAttempts = 3
	f := NewFulfillment(cfg)
	ctx := context.Background()
	change, err := f.Accept(ctx, &o, &s, now)
	if err != nil {
		t.Fatal(err)
	}
	original := change.Commands[0]
	for expected := 2; expected <= 3; expected++ {
		due := s.NextAttempt
		f = NewFulfillment(cfg) // Process restart keeps only persisted state.
		change, err = f.Tick(ctx, &o, &s, due)
		if err != nil || change.ReplayCommandID != original.MessageID || s.Attempts != expected || len(change.Commands) != 0 || !s.NextAttempt.After(due) {
			t.Fatal(change, s, err)
		}
	}
	if _, err = f.Tick(ctx, &o, &s, s.NextAttempt); err != nil || s.State != domain.SagaNeedsAttention {
		t.Fatal(s, err)
	}
	change, err = f.Retry(ctx, &o, &s, s.NextAttempt)
	if err != nil || s.State != domain.SagaCreatingDelivery || change.ReplayCommandID != original.MessageID || change.Commands[0].AggregateVersion != 2 || change.Commands[0].MessageID == original.MessageID {
		t.Fatal(change, s, err)
	}
}
func TestRestaurantRejectionAndUnsafeCancellation(t *testing.T) {
	o, s, now := fulfillmentOrder(t)
	f := NewFulfillment(DefaultSagaConfig())
	ctx := context.Background()
	change, err := f.Cancel(ctx, &o, &s, "restaurant_rejected", now)
	if err != nil || change.EventKind != events.OrderRejected || len(change.Commands) != 0 || s.State != domain.SagaCancelled {
		t.Fatal(change, s, err)
	}
	if _, err = f.Prepare(&o, &s, now); err == nil {
		t.Fatal("prepared rejected order")
	}
	o, s, now = fulfillmentOrder(t)
	if _, err = f.Accept(ctx, &o, &s, now); err != nil {
		t.Fatal(err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryCreated, 1, now)
	advanceSaga(t, f, &o, &s, delivery.CourierAssigned, 2, now)
	if _, err = f.Prepare(&o, &s, now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Ready(&o, &s, now); err != nil {
		t.Fatal(err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryPickedUp, 3, now)
	if _, err = f.Cancel(ctx, &o, &s, "restaurant_rejected", now); !errors.Is(err, domain.ErrCannotCancel) {
		t.Fatal(err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryCancellationRejected, 4, now)
	if s.State != domain.SagaNeedsAttention || o.Snapshot().Status != domain.OnTheWay {
		t.Fatal(s, o.Snapshot())
	}
	if _, err = f.Retry(ctx, &o, &s, now); err != nil || s.State != domain.SagaOnTheWay {
		t.Fatal(s, err)
	}
}

func TestLateDeliveryCreatedCannotEraseEscalatedCompensation(t *testing.T) {
	o, s, now := fulfillmentOrder(t)
	cfg := DefaultSagaConfig()
	cfg.MaxAttempts = 1
	f := NewFulfillment(cfg)
	ctx := context.Background()
	if _, err := f.Accept(ctx, &o, &s, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Cancel(ctx, &o, &s, "restaurant_rejected", now); err != nil {
		t.Fatal(err)
	}
	cancelID := s.CommandID
	if _, err := f.Tick(ctx, &o, &s, s.NextAttempt); err != nil || s.State != domain.SagaNeedsAttention {
		t.Fatal(s, err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryCreated, 1, now)
	if s.State != domain.SagaNeedsAttention || s.CommandID != cancelID || s.CommandType != commands.CancelDelivery || s.CancelReason != "restaurant_rejected" || !o.Snapshot().CancellationRequested {
		t.Fatal("late create erased pending compensation", s)
	}
	e := deliveryFact(delivery.DeliveryCancelled, 2, now)
	change, err := f.HandleDelivery(ctx, &o, &s, e, now)
	if err != nil || change.EventKind != events.OrderRejected || s.State != domain.SagaCancelled {
		t.Fatal(change, s, err)
	}
}
