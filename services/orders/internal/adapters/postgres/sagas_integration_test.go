package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestSagaAtomicAcceptRetryRestartAndOperatorRecovery(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	cfg := application.DefaultSagaConfig()
	cfg.MaxAttempts = 2
	f := application.NewFulfillment(cfg)
	r := NewOrders(pool, f)
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}}, f)
	o, err := r.Create(ctx, "saga", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.Snapshot().ID
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_command CHECK(event_type<>'CreateDelivery')`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Change(ctx, 10, 1, id, "accept"); err == nil {
		t.Fatal("accepted without saga command")
	}
	current, err := r.Get(ctx, 7, id)
	if err != nil || current.Snapshot().Status != domain.WaitingRestaurant {
		t.Fatal(current, err)
	}
	saga, err := r.Saga(ctx, 1, id)
	if err != nil || saga.State != domain.SagaWaitingRestaurant {
		t.Fatal(saga, err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_command`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Change(ctx, 10, 1, id, "accept"); err != nil {
		t.Fatal(err)
	}
	saga, err = r.Saga(ctx, 1, id)
	if err != nil || saga.State != domain.SagaCreatingDelivery || saga.CommandID == "" {
		t.Fatal(saga, err)
	}
	var initial []byte
	if err = pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE event_id=$1`, saga.CommandID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	var command commands.Command
	if err = json.Unmarshal(initial, &command); err != nil || command.Validate() != nil {
		t.Fatal(err)
	}
	// Restart and retry preserve the command's identity and immutable envelope.
	r = NewOrders(pool, f)
	if _, err = pool.Exec(ctx, `UPDATE outbox_events SET status='PUBLISHED',published_at=now() WHERE event_id=$1`, saga.CommandID); err != nil {
		t.Fatal(err)
	}
	retried, found, err := r.StepSaga(ctx, saga.NextAttempt.Add(time.Millisecond))
	if err != nil || !found || retried.Attempts != 2 || retried.CommandID != saga.CommandID {
		t.Fatal(retried, found, err)
	}
	var body []byte
	var status string
	if err = pool.QueryRow(ctx, `SELECT body,status FROM outbox_events WHERE event_id=$1`, saga.CommandID).Scan(&body, &status); err != nil || string(body) != string(initial) || status != "PENDING" {
		t.Fatal(status, err)
	}
	escalated, found, err := r.StepSaga(ctx, retried.NextAttempt.Add(time.Millisecond))
	if err != nil || !found || escalated.State != domain.SagaNeedsAttention || escalated.Reason == "" {
		t.Fatal(escalated, found, err)
	}
	svc = application.NewRestaurants(r, map[int64][]int64{10: {1}}, f)
	if _, err = svc.Change(ctx, 99, 1, id, "retry"); !errors.Is(err, application.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = svc.Change(ctx, 10, 1, id, "retry"); err != nil {
		t.Fatal(err)
	}
	recovered, err := r.Saga(ctx, 1, id)
	if err != nil || recovered.State != domain.SagaCreatingDelivery || recovered.CommandSequence != 2 {
		t.Fatal(recovered, err)
	}
	bindAndAssign(t, r, id)
	if _, err = svc.Change(ctx, 10, 1, id, "prepare"); err != nil {
		t.Fatal("recovery did not unlock cooking", err)
	}
}
func TestSagaAssignmentTimeoutCancellationAndPreparedEscalation(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_preparing", true: "after_preparing"}[prepared], func(t *testing.T) {
			pool := integrationPool(t)
			ctx := context.Background()
			cfg := application.DefaultSagaConfig()
			cfg.CancelBeforePreparing = true
			f := application.NewFulfillment(cfg)
			r := NewOrders(pool, f)
			svc := application.NewRestaurants(r, map[int64][]int64{10: {1}}, f)
			o, err := r.Create(ctx, "timeout", strings.Repeat("a", 64), newOrder(t))
			if err != nil {
				t.Fatal(err)
			}
			id := o.Snapshot().ID
			if _, err = svc.Change(ctx, 10, 1, id, "accept"); err != nil {
				t.Fatal(err)
			}
			e := delivery.Event{EventID: events.NewID(), EventType: delivery.DeliveryCreated, SchemaVersion: 1, AggregateID: 99, AggregateVersion: 1, CorrelationID: "timeout", OccurredAt: time.Now().UTC(), Payload: delivery.Payload{OrderID: id, AmountToCollectMinor: 13100, Currency: "TJS"}}
			if err = r.ProcessDelivery(ctx, e); err != nil {
				t.Fatal(err)
			}
			if prepared {
				e.EventID = events.NewID()
				e.EventType = delivery.CourierAssigned
				e.AggregateVersion = 2
				e.Payload.CourierID = 7
				if err = r.ProcessDelivery(ctx, e); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.Change(ctx, 10, 1, id, "prepare"); err != nil {
					t.Fatal(err)
				}
				e.EventID = events.NewID()
				e.EventType = delivery.CourierReleased
				e.AggregateVersion = 3
				e.Payload.CourierID = 0
				if err = r.ProcessDelivery(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			s, err := r.Saga(ctx, 1, id)
			if err != nil {
				t.Fatal(err)
			}
			next, found, err := NewOrders(pool, f).StepSaga(ctx, s.Deadline.Add(time.Millisecond))
			if err != nil || !found {
				t.Fatal(next, found, err)
			}
			current, err := r.Get(ctx, 7, id)
			if err != nil {
				t.Fatal(err)
			}
			if prepared {
				if next.State != domain.SagaNeedsAttention || next.Reason != "courier_missing_after_preparation" || current.Snapshot().Status != domain.Preparing || current.Snapshot().CancellationRequested {
					t.Fatal(next, current.Snapshot())
				}
				var n int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='CancelDelivery'`).Scan(&n); err != nil || n != 0 {
					t.Fatal(n, err)
				}
			} else {
				if next.State != domain.SagaCancelling || !current.Snapshot().CancellationRequested || current.Snapshot().Status == domain.Cancelled {
					t.Fatal(next, current.Snapshot())
				}
				e.EventID = events.NewID()
				e.EventType = delivery.DeliveryCancelled
				e.AggregateVersion = 2
				if err = r.ProcessDelivery(ctx, e); err != nil {
					t.Fatal(err)
				}
				if err = r.ProcessDelivery(ctx, e); err != nil {
					t.Fatal("duplicate cancellation", err)
				}
				final, err := r.Saga(ctx, 1, id)
				if err != nil || final.State != domain.SagaCancelled {
					t.Fatal(final, err)
				}
				current, err = r.Get(ctx, 7, id)
				if err != nil || current.Snapshot().Status != domain.Cancelled {
					t.Fatal(current, err)
				}
			}
		})
	}
}
func TestSagaOutOfOrderAndStaleDeliveryFacts(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	r := NewOrders(pool)
	svc := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	o, err := r.Create(ctx, "ordered", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.Snapshot().ID
	if _, err = svc.Change(ctx, 10, 1, id, "accept"); err != nil {
		t.Fatal(err)
	}
	e := delivery.Event{EventID: events.NewID(), EventType: delivery.CourierAssigned, SchemaVersion: 1, AggregateID: 99, AggregateVersion: 2, CorrelationID: "ordered", OccurredAt: time.Now().UTC(), Payload: delivery.Payload{OrderID: id, CourierID: 7, AmountToCollectMinor: 13100, Currency: "TJS"}}
	if err = r.ProcessDelivery(ctx, e); !errors.Is(err, application.ErrEventGap) {
		t.Fatal("gap was accepted", err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM delivery_inbox`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	created := e
	created.EventID = events.NewID()
	created.EventType = delivery.DeliveryCreated
	created.AggregateVersion = 1
	created.Payload.CourierID = 0
	if err = r.ProcessDelivery(ctx, created); err != nil {
		t.Fatal(err)
	}
	if err = r.ProcessDelivery(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = r.ProcessDelivery(ctx, created); err != nil {
		t.Fatal("stale duplicate", err)
	}
	current, err := r.Get(ctx, 7, id)
	if err != nil || current.Snapshot().CourierID != 7 || current.Snapshot().Status != domain.Accepted {
		t.Fatal(current, err)
	}
}
