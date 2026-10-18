package application

import (
	"context"
	"testing"
	"time"

	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestDispatchSnapshotDeadlineAndSagaGate(t *testing.T) {
	o, _, now := fulfillmentOrder(t)
	state := o.Snapshot()
	state.RestaurantLocation = &geo.Point{Latitude: 38.56, Longitude: 68.77}
	var err error
	o, err = domain.Restore(state)
	if err != nil {
		t.Fatal(err)
	}
	saga := domain.NewSaga(state.ID, now)
	f := NewFulfillment(DefaultSagaConfig())
	change, err := f.Accept(context.Background(), &o, &saga, now)
	if err != nil {
		t.Fatal(err)
	}
	command := change.Commands[0]
	if command.Payload.CustomerID != state.CustomerID || command.Payload.CustomerID <= 0 {
		t.Fatal("tracking owner must come from the aggregate", command)
	}
	if command.Payload.RestaurantLocation == nil || *command.Payload.RestaurantLocation != *state.RestaurantLocation || command.Payload.DispatchDeadline == nil {
		t.Fatal(command)
	}
	deadline := *command.Payload.DispatchDeadline
	// A delayed response must not extend the already published automatic dispatch window.
	advanceSaga(t, f, &o, &saga, delivery.DeliveryCreated, 1, now.Add(time.Minute))
	if !saga.Deadline.Equal(deadline) {
		t.Fatal(saga.Deadline, deadline)
	}
	if _, err = f.Prepare(&o, &saga, now); err == nil {
		t.Fatal("preparation gate bypassed")
	}
	advanceSaga(t, f, &o, &saga, delivery.CourierAssigned, 2, now.Add(2*time.Minute))
	if _, err = f.Prepare(&o, &saga, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizedSagaResumeIssuesNewDispatchWindow(t *testing.T) {
	o, s, now := fulfillmentOrder(t)
	f := NewFulfillment(DefaultSagaConfig())
	if _, err := f.Accept(context.Background(), &o, &s, now); err != nil {
		t.Fatal(err)
	}
	advanceSaga(t, f, &o, &s, delivery.DeliveryCreated, 1, now)
	original := *s.DispatchDeadline
	if _, err := f.Tick(context.Background(), &o, &s, original.Add(time.Second)); err != nil || s.State != domain.SagaNeedsAttention {
		t.Fatal(s, err)
	}
	change, err := f.Retry(context.Background(), &o, &s, original.Add(time.Minute))
	if err != nil || len(change.Commands) != 1 || s.State != domain.SagaCreatingDelivery || !s.DispatchDeadline.After(original) {
		t.Fatal(change, s, err)
	}
}
