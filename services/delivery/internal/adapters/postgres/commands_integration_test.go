package postgres

import (
	"context"
	"errors"
	"testing"

	commands "example.com/food/contracts/delivery/commands/v1"
	events "example.com/food/contracts/delivery/v1"
	"example.com/food/delivery/internal/application"
	"example.com/food/delivery/internal/domain"
)

func TestCancellationTombstoneAndCommandReplies(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	cancel := createCommand(42, 2, commands.CancelDelivery)
	if err := r.ProcessCommand(ctx, cancel); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessCommand(ctx, cancel); err != nil {
		t.Fatal("duplicate cancel", err)
	}
	late := createCommand(42, 1, commands.CreateDelivery)
	if err := r.ProcessCommand(ctx, late); err != nil {
		t.Fatal(err)
	}
	// Even a newer Create cannot resurrect a physically cancelled job.
	if err := r.ProcessCommand(ctx, createCommand(42, 3, commands.CreateDelivery)); err != nil {
		t.Fatal(err)
	}
	list, err := r.List(ctx, 0, 0, 100)
	if err != nil || len(list) != 1 || list[0].Status != domain.Cancelled {
		t.Fatal(list, err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM deliveries`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var messageID string
	if err = pool.QueryRow(ctx, `SELECT response_event_id::text FROM command_inbox WHERE message_id=$1`, cancel.MessageID).Scan(&messageID); err != nil || messageID == "" {
		t.Fatal(messageID, err)
	}
}
func TestCourierOfflineDeclineAndSafeCompensation(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	d := accepted(t, r, 42)
	c := makeCourier(t, s)
	if _, err := s.SetAvailability(ctx, c.ID, c.ID, domain.Offline); err != nil {
		t.Fatal(err)
	}
	available, err := s.Couriers(ctx, 20, 0, 100, true)
	if err != nil || len(available) != 0 {
		t.Fatal(available, err)
	}
	if _, err = s.Assign(ctx, 20, d.ID, c.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("offline courier assigned", err)
	}
	if _, err = s.SetAvailability(ctx, c.ID, c.ID, domain.Available); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetAvailability(ctx, c.ID, c.ID, domain.Offline); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("busy courier went offline", err)
	}
	if _, err = s.Decline(ctx, c.ID+1, d.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.Decline(ctx, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.Status != domain.PendingAssignment || got.CourierID != 0 {
		t.Fatal(got, err)
	}
	if _, err = s.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal("courier was not released", err)
	}
	ready(t, r, 42)
	if err = r.ProcessCommand(ctx, createCommand(42, 2, commands.CancelDelivery)); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(ctx, 0, d.ID)
	if err != nil || got.Status != domain.Assigned {
		t.Fatal("cooked food was cancelled", got, err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type=$1`, events.DeliveryCancellationRejected).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = s.Pickup(ctx, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = r.ProcessCommand(ctx, createCommand(42, 3, commands.CancelDelivery)); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(ctx, 0, d.ID)
	if err != nil || got.Status != domain.PickedUp {
		t.Fatal("picked up order was cancelled", got, err)
	}
	if _, err = s.Complete(ctx, c.ID, d.ID, true, "receipt"); err != nil {
		t.Fatal(err)
	}
	if err = r.ProcessCommand(ctx, createCommand(42, 4, commands.CreateDelivery)); err != nil {
		t.Fatal("completed snapshot", err)
	}
	got, err = r.Get(ctx, 0, d.ID)
	if err != nil || got.Confirmation.ID <= 0 || got.Confirmation.ReceiptReference != "receipt" {
		t.Fatal(got, err)
	}
}
func TestCancelPendingTaskAtomicallyReleasesCourier(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	d := accepted(t, r, 42)
	c := makeCourier(t, s)
	if _, err := s.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessCommand(ctx, createCommand(42, 2, commands.CancelDelivery)); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.Status != domain.Cancelled {
		t.Fatal(got, err)
	}
	couriers, err := s.Couriers(ctx, 20, 0, 100, true)
	if err != nil || len(couriers) != 1 || couriers[0].UpdatedAt.Before(couriers[0].CreatedAt) {
		t.Fatal(couriers, err)
	}
}
