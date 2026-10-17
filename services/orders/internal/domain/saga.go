package domain

import (
	"errors"
	"time"
)

const (
	SagaWaitingRestaurant = "WAITING_RESTAURANT"
	SagaCreatingDelivery  = "CREATING_DELIVERY"
	SagaWaitingCourier    = "WAITING_COURIER"
	SagaCourierAssigned   = "COURIER_ASSIGNED"
	SagaPreparing         = "PREPARING"
	SagaReady             = "READY_FOR_PICKUP"
	SagaOnTheWay          = "ON_THE_WAY"
	SagaCancelling        = "CANCELLING"
	SagaCompleted         = "COMPLETED"
	SagaCancelled         = "CANCELLED"
	SagaNeedsAttention    = "NEEDS_ATTENTION"
)

var ErrSagaTransition = errors.New("invalid fulfillment saga transition")

// Saga tracks remote progress and durable scheduling, not a distributed SQL transaction.
type Saga struct {
	OrderID              int64
	State                string
	Version              int64
	DeliveryID           int64
	DeliveryVersion      int64
	CourierID            int64
	CommandID            string
	CommandType          string
	CommandSequence      int64
	Attempts             int
	Deadline             time.Time
	NextAttempt          time.Time
	Reason               string
	CancelReason         string
	LegacyReconciliation bool
	CorrelationID        string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func NewSaga(order int64, now time.Time) Saga {
	return Saga{OrderID: order, State: SagaWaitingRestaurant, Version: 1, Deadline: now, NextAttempt: now, CreatedAt: now, UpdatedAt: now}
}
func (s Saga) Terminal() bool { return s.State == SagaCompleted || s.State == SagaCancelled }
func (s Saga) Scheduled() bool {
	return !s.Terminal() && s.State != SagaWaitingRestaurant && s.State != SagaNeedsAttention
}
func (s *Saga) Move(target string, now time.Time) error {
	if target == s.State {
		s.Touch(now)
		return nil
	}
	allowed := false
	switch target {
	case SagaCreatingDelivery:
		allowed = s.State == SagaWaitingRestaurant || s.State == SagaNeedsAttention
	case SagaWaitingCourier:
		allowed = s.State == SagaCreatingDelivery || s.State == SagaCourierAssigned || s.State == SagaNeedsAttention
	case SagaCourierAssigned:
		allowed = s.State == SagaWaitingCourier || s.State == SagaCreatingDelivery || s.State == SagaNeedsAttention
	case SagaPreparing:
		allowed = s.State == SagaCourierAssigned || s.State == SagaNeedsAttention || s.State == SagaCreatingDelivery || s.State == SagaWaitingCourier
	case SagaReady:
		allowed = s.State == SagaPreparing || s.State == SagaNeedsAttention || s.State == SagaCreatingDelivery
	case SagaOnTheWay:
		allowed = s.State == SagaReady || s.State == SagaNeedsAttention || s.State == SagaCreatingDelivery
	case SagaCancelling:
		allowed = !s.Terminal() && s.State != SagaWaitingRestaurant
	case SagaCancelled:
		allowed = s.State == SagaWaitingRestaurant || s.State == SagaCancelling
	case SagaCompleted:
		allowed = s.State == SagaOnTheWay || s.State == SagaNeedsAttention
	case SagaNeedsAttention:
		allowed = !s.Terminal()
	}
	if !allowed {
		return ErrSagaTransition
	}
	s.State = target
	s.Touch(now)
	return nil
}
func (s *Saga) Touch(now time.Time) {
	s.Version++
	if now.Before(s.UpdatedAt) {
		now = s.UpdatedAt
	}
	s.UpdatedAt = now.UTC()
}
func (s *Saga) Escalate(reason string, now time.Time) {
	if !s.Terminal() {
		s.State = SagaNeedsAttention
		s.Reason = reason
		s.Touch(now)
	}
}
