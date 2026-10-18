package application

import (
	"context"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

type SagaConfig struct {
	CreateTimeout, AssignmentTimeout, ProgressTimeout, CancelTimeout time.Duration
	RetryBase, RetryMax, PollInterval                                time.Duration
	MaxAttempts                                                      int
	CancelBeforePreparing                                            bool
}

func DefaultSagaConfig() SagaConfig {
	return SagaConfig{CreateTimeout: 5 * time.Minute, AssignmentTimeout: 10 * time.Minute, ProgressTimeout: 30 * time.Minute, CancelTimeout: 5 * time.Minute, RetryBase: time.Second, RetryMax: 30 * time.Second, PollInterval: time.Second, MaxAttempts: 20}
}

type Fulfillment struct{ cfg SagaConfig }

func NewFulfillment(cfg SagaConfig) *Fulfillment { return &Fulfillment{cfg} }

type FulfillmentChange struct {
	EventKind       string
	Commands        []commands.Command
	ReplayCommandID string
}
type FulfillmentMutation func(*domain.Order, *domain.Saga) (FulfillmentChange, error)

func (f *Fulfillment) command(ctx context.Context, o domain.Order, s *domain.Saga, kind string, now time.Time) commands.Command {
	id := s.CorrelationID
	if !correlation.Valid(id) {
		id = correlation.ID(ctx)
	}
	if !correlation.Valid(id) {
		id = events.NewID()
	}
	s.CorrelationID = id
	s.CommandID = events.NewID()
	s.CommandType = kind
	s.CommandSequence++
	s.Attempts = 1
	s.NextAttempt = now.Add(RetryDelay(1, f.cfg.RetryBase, f.cfg.RetryMax))
	v := o.Snapshot()
	if kind == commands.CreateDelivery {
		deadline := now.Add(f.cfg.AssignmentTimeout).UTC()
		s.DispatchDeadline = &deadline
	}
	fulfillmentType := ""
	if !v.LegacyDeliveryAddress {
		fulfillmentType = string(v.FulfillmentType)
	}
	return commands.Command{MessageID: s.CommandID, CommandType: kind, CorrelationID: id, AggregateID: v.ID, AggregateVersion: s.CommandSequence, SchemaVersion: 1, OccurredAt: now.UTC(), Payload: commands.Payload{RestaurantLocation: v.RestaurantLocation, DispatchDeadline: s.DispatchDeadline, FulfillmentType: fulfillmentType, DeliveryAddress: v.DeliveryAddress, OrderID: v.ID, OrderVersion: v.Version, AmountToCollectMinor: v.SubtotalMinor, Currency: v.Currency}}
}
func (f *Fulfillment) Accept(ctx context.Context, o *domain.Order, s *domain.Saga, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType == domain.Pickup {
		return FulfillmentChange{EventKind: events.OrderAccepted}, o.Transition(domain.Accepted, now)
	}
	if s.State != domain.SagaWaitingRestaurant {
		return FulfillmentChange{}, domain.ErrInvalidTransition
	}
	if err := o.Transition(domain.Accepted, now); err != nil {
		return FulfillmentChange{}, err
	}
	if err := s.Move(domain.SagaCreatingDelivery, now); err != nil {
		return FulfillmentChange{}, err
	}
	s.Deadline = now.Add(f.cfg.CreateTimeout)
	return FulfillmentChange{EventKind: events.OrderAccepted, Commands: []commands.Command{f.command(ctx, *o, s, commands.CreateDelivery, now)}}, nil
}
func (f *Fulfillment) Cancel(ctx context.Context, o *domain.Order, s *domain.Saga, reason string, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType == domain.Pickup {
		if err := o.Cancel(now); err != nil {
			return FulfillmentChange{}, err
		}
		kind := events.OrderCancelled
		if reason == "restaurant_rejected" {
			kind = events.OrderRejected
		}
		return FulfillmentChange{EventKind: kind}, nil
	}
	if o.Snapshot().Status == domain.WaitingRestaurant || o.Snapshot().Status == domain.PendingPayment {
		if err := o.Cancel(now); err != nil {
			return FulfillmentChange{}, err
		}
		if err := s.Move(domain.SagaCancelled, now); err != nil {
			return FulfillmentChange{}, err
		}
		s.Reason = reason
		s.CancelReason = reason
		kind := events.OrderCancelled
		if reason == "restaurant_rejected" {
			kind = events.OrderRejected
		}
		return FulfillmentChange{EventKind: kind}, nil
	}
	if s.State == domain.SagaCancelling {
		return FulfillmentChange{}, nil
	}
	if err := o.RequestCancellation(now); err != nil {
		return FulfillmentChange{}, err
	}
	if err := s.Move(domain.SagaCancelling, now); err != nil {
		return FulfillmentChange{}, err
	}
	s.Reason = reason
	s.CancelReason = reason
	oldCommand := s.CommandID
	s.Deadline = now.Add(f.cfg.CancelTimeout)
	return FulfillmentChange{EventKind: events.OrderCancellationRequested, ReplayCommandID: oldCommand, Commands: []commands.Command{f.command(ctx, *o, s, commands.CancelDelivery, now)}}, nil
}
func (f *Fulfillment) Prepare(o *domain.Order, s *domain.Saga, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType == domain.Pickup {
		return FulfillmentChange{EventKind: events.OrderPreparing}, o.Transition(domain.Preparing, now)
	}
	if s.State != domain.SagaCourierAssigned || s.CourierID <= 0 {
		return FulfillmentChange{}, domain.ErrInvalidTransition
	}
	if err := o.Transition(domain.Preparing, now); err != nil {
		return FulfillmentChange{}, err
	}
	if err := s.Move(domain.SagaPreparing, now); err != nil {
		return FulfillmentChange{}, err
	}
	f.wait(s, f.cfg.ProgressTimeout, now)
	return FulfillmentChange{EventKind: events.OrderPreparing}, nil
}
func (f *Fulfillment) Ready(o *domain.Order, s *domain.Saga, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType == domain.Pickup {
		return FulfillmentChange{EventKind: events.OrderReadyForPickup}, o.Transition(domain.ReadyForPickup, now)
	}
	if s.State != domain.SagaPreparing {
		return FulfillmentChange{}, domain.ErrSagaTransition
	}
	if err := o.Transition(domain.ReadyForPickup, now); err != nil {
		return FulfillmentChange{}, err
	}
	if err := s.Move(domain.SagaReady, now); err != nil {
		return FulfillmentChange{}, err
	}
	f.wait(s, f.cfg.ProgressTimeout, now)
	return FulfillmentChange{EventKind: events.OrderReadyForPickup}, nil
}
func (f *Fulfillment) wait(s *domain.Saga, d time.Duration, now time.Time) {
	s.Deadline = now.Add(d)
	s.NextAttempt = s.Deadline
	s.Attempts = 0
	s.CommandID = ""
	s.CommandType = ""
	s.Reason = ""
}

// The dispatch window is published before the remote creation and cannot be
// extended by delayed responses or refusals. Historical Sagas have no such field.
func (f *Fulfillment) waitAssignment(s *domain.Saga, now time.Time) {
	f.wait(s, f.cfg.AssignmentTimeout, now)
	if s.DispatchDeadline != nil {
		s.Deadline = *s.DispatchDeadline
		s.NextAttempt = s.Deadline
	}
}

func (f *Fulfillment) Tick(ctx context.Context, o *domain.Order, s *domain.Saga, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType != domain.Delivery {
		return FulfillmentChange{}, domain.ErrInvalidTransition
	}
	if !s.Scheduled() || now.Before(s.NextAttempt) {
		return FulfillmentChange{}, nil
	}
	if s.State == domain.SagaCreatingDelivery && s.CommandID == "" {
		s.Deadline = now.Add(f.cfg.CreateTimeout)
		s.Touch(now)
		return FulfillmentChange{Commands: []commands.Command{f.command(ctx, *o, s, commands.CreateDelivery, now)}}, nil
	}
	if s.State == domain.SagaCreatingDelivery || s.State == domain.SagaCancelling {
		if !now.Before(s.Deadline) || s.Attempts >= f.cfg.MaxAttempts {
			s.Escalate("command_response_timeout", now)
			return FulfillmentChange{}, nil
		}
		s.Attempts++
		s.NextAttempt = now.Add(RetryDelay(s.Attempts, f.cfg.RetryBase, f.cfg.RetryMax))
		s.Touch(now)
		return FulfillmentChange{ReplayCommandID: s.CommandID}, nil
	}
	if now.Before(s.Deadline) {
		s.NextAttempt = s.Deadline
		return FulfillmentChange{}, nil
	}
	if s.CourierID == 0 && !o.PreparationStarted() && f.cfg.CancelBeforePreparing {
		return f.Cancel(ctx, o, s, "courier_assignment_timeout", now)
	}
	reason := "fulfillment_progress_timeout"
	if s.CourierID == 0 {
		reason = "courier_assignment_timeout"
	}
	if o.PreparationStarted() && s.CourierID == 0 {
		reason = "courier_missing_after_preparation"
	}
	s.Escalate(reason, now)
	return FulfillmentChange{}, nil
}
func (f *Fulfillment) Retry(ctx context.Context, o *domain.Order, s *domain.Saga, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType != domain.Delivery {
		return FulfillmentChange{}, domain.ErrInvalidTransition
	}
	if s.State != domain.SagaNeedsAttention {
		return FulfillmentChange{}, domain.ErrSagaTransition
	}
	// Acknowledging an exception never fabricates delivery or POS payment.
	if o.Snapshot().CancellationRequested && (o.PreparationStarted() || s.Reason == "cancellation_rejected") {
		before := o.Snapshot().Version
		if err := o.Resume(now); err != nil {
			return FulfillmentChange{}, err
		}
		phase := phaseFor(*o, s.CourierID)
		if err := s.Move(phase, now); err != nil {
			return FulfillmentChange{}, err
		}
		f.wait(s, f.cfg.ProgressTimeout, now)
		change := FulfillmentChange{}
		if before != o.Snapshot().Version {
			change.EventKind = events.OrderResumed
		}
		return change, nil
	}
	if o.Snapshot().CancellationRequested {
		if err := s.Move(domain.SagaCancelling, now); err != nil {
			return FulfillmentChange{}, err
		}
		s.Deadline = now.Add(f.cfg.CancelTimeout)
		old := s.CommandID
		return FulfillmentChange{ReplayCommandID: old, Commands: []commands.Command{f.command(ctx, *o, s, commands.CancelDelivery, now)}}, nil
	}
	if s.DeliveryID == 0 || s.LegacyReconciliation || s.CommandType == commands.CreateDelivery {
		old := s.CommandID
		if err := s.Move(domain.SagaCreatingDelivery, now); err != nil {
			return FulfillmentChange{}, err
		}
		s.Deadline = now.Add(f.cfg.CreateTimeout)
		s.Reason = ""
		return FulfillmentChange{ReplayCommandID: old, Commands: []commands.Command{f.command(ctx, *o, s, commands.CreateDelivery, now)}}, nil
	}
	if s.CourierID == 0 && !o.PreparationStarted() {
		s.Deadline = now.Add(f.cfg.CreateTimeout)
		if err := s.Move(domain.SagaCreatingDelivery, now); err != nil {
			return FulfillmentChange{}, err
		}
		return FulfillmentChange{Commands: []commands.Command{f.command(ctx, *o, s, commands.CreateDelivery, now)}}, nil
	}
	if err := s.Move(phaseFor(*o, s.CourierID), now); err != nil {
		return FulfillmentChange{}, err
	}
	duration := f.cfg.ProgressTimeout
	if s.CourierID == 0 {
		duration = f.cfg.AssignmentTimeout
	}
	f.wait(s, duration, now)
	return FulfillmentChange{}, nil
}
func phaseFor(o domain.Order, courier int64) string {
	switch o.Snapshot().Status {
	case domain.Preparing:
		return domain.SagaPreparing
	case domain.ReadyForPickup:
		return domain.SagaReady
	case domain.OnTheWay:
		return domain.SagaOnTheWay
	}
	if courier > 0 {
		return domain.SagaCourierAssigned
	}
	return domain.SagaWaitingCourier
}

// HandleDelivery is the orchestrator. Adapters only fence identities/versions and persist its result.
func (f *Fulfillment) HandleDelivery(ctx context.Context, o *domain.Order, s *domain.Saga, e delivery.Event, now time.Time) (FulfillmentChange, error) {
	if o.Snapshot().FulfillmentType != domain.Delivery {
		return FulfillmentChange{}, domain.ErrInvalidTransition
	}
	if e.Payload.AmountToCollectMinor != o.Snapshot().SubtotalMinor || e.Payload.Currency != o.Snapshot().Currency {
		return FulfillmentChange{}, domain.ErrPaymentMismatch
	}
	if s.Terminal() {
		return FulfillmentChange{}, nil
	}
	kind := e.EventType
	if kind == delivery.DeliveryStateReported {
		switch e.Payload.Status {
		case "PENDING_ASSIGNMENT":
			kind = delivery.DeliveryCreated
		case "ASSIGNED":
			kind = delivery.CourierAssigned
		case "PICKED_UP":
			kind = delivery.DeliveryPickedUp
		case "DELIVERED":
			kind = delivery.DeliveryCompleted
		case "CANCELLED":
			kind = delivery.DeliveryCancelled
		}
	}
	change := FulfillmentChange{}
	switch kind {
	case delivery.DeliveryCreated:
		if o.Snapshot().CourierID > 0 {
			if err := o.ReleaseCourier(now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderCourierReleased
		}
		s.CourierID = 0
		if o.Snapshot().Status == domain.Accepted && o.Snapshot().CourierID == 0 {
			if err := o.AwaitCourier(now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderWaitingCourier
		}
		if !o.Snapshot().CancellationRequested {
			if err := s.Move(phaseFor(*o, 0), now); err != nil {
				return change, err
			}
			f.waitAssignment(s, now)
		}
	case delivery.CourierAssigned:
		if o.Snapshot().CourierID != e.Payload.CourierID {
			if err := o.AssignCourier(e.Payload.CourierID, now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderCourierAssigned
		}
		s.CourierID = e.Payload.CourierID
		s.LegacyReconciliation = false
		if !o.Snapshot().CancellationRequested {
			if err := s.Move(phaseFor(*o, s.CourierID), now); err != nil {
				return change, err
			}
			f.wait(s, f.cfg.ProgressTimeout, now)
		}
	case delivery.CourierReleased:
		if o.Snapshot().CourierID != 0 {
			if err := o.ReleaseCourier(now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderCourierReleased
		} else if s.LegacyReconciliation && o.Snapshot().Status == domain.Accepted {
			if err := o.AwaitCourier(now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderCourierReleased
		} else if !s.LegacyReconciliation {
			return change, domain.ErrInvalidTransition
		}
		s.CourierID = 0
		if !o.Snapshot().CancellationRequested {
			target := phaseFor(*o, 0)
			if err := s.Move(target, now); err != nil {
				return change, err
			}
			f.waitAssignment(s, now)
		}
	case delivery.DeliveryPickedUp:
		if o.Snapshot().Status != domain.OnTheWay {
			if err := o.ConfirmPickup(e.Payload.CourierID, s.LegacyReconciliation, now); err != nil {
				return change, err
			}
			change.EventKind = events.OrderOnTheWay
		}
		s.CourierID = e.Payload.CourierID
		s.LegacyReconciliation = false
		if o.Snapshot().CancellationRequested {
			s.Escalate("picked_up_during_cancellation", now)
		} else {
			if err := s.Move(domain.SagaOnTheWay, now); err != nil {
				return change, err
			}
			f.wait(s, f.cfg.ProgressTimeout, now)
		}
	case delivery.DeliveryCompleted:
		if s.State == domain.SagaCreatingDelivery && s.LegacyReconciliation && o.Snapshot().Status == domain.OnTheWay {
			if err := s.Move(domain.SagaOnTheWay, now); err != nil {
				return change, err
			}
		}
		p := e.Payload.Confirmation
		if p == nil {
			return change, domain.ErrPaymentMismatch
		}
		if err := o.Complete(p.AmountMinor, p.Currency, p.PaymentMethod, now); err != nil {
			return change, err
		}
		if err := s.Move(domain.SagaCompleted, now); err != nil {
			return change, err
		}
		s.LegacyReconciliation = false
		change.EventKind = events.OrderDelivered
	case delivery.DeliveryCancelled:
		if o.PreparationStarted() {
			s.Escalate("delivery_cancelled_after_preparation", now)
			return change, nil
		}
		if err := o.ConfirmCancellation(now); err != nil {
			return change, err
		}
		if s.State != domain.SagaCancelling {
			if err := s.Move(domain.SagaCancelling, now); err != nil {
				return change, err
			}
		}
		reason := s.CancelReason
		if err := s.Move(domain.SagaCancelled, now); err != nil {
			return change, err
		}
		s.LegacyReconciliation = false
		change.EventKind = events.OrderCancelled
		if reason == "restaurant_rejected" {
			change.EventKind = events.OrderRejected
		}
	case delivery.DeliveryCancellationRejected:
		s.Escalate("cancellation_rejected", now)
	default:
		return change, domain.ErrSagaTransition
	}
	s.Touch(now)
	return change, nil
}
