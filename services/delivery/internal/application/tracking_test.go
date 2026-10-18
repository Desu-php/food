package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type trackingRepo struct {
	state TrackingState
	err   error
}

func (r *trackingRepo) TrackingState(_ context.Context, customer, order int64) (TrackingState, error) {
	if customer != 7 || order != 42 {
		return TrackingState{}, domain.ErrNotFound
	}
	return r.state, r.err
}
func (r *trackingRepo) CourierLocationAllowed(context.Context, int64) error { return r.err }

type liveLocations struct {
	l         domain.Location
	reads     int
	err       error
	afterRead func()
}

func (l *liveLocations) Store(_ context.Context, v domain.Location) error { l.l = v; return l.err }
func (l *liveLocations) Latest(context.Context, int64) (domain.Location, bool, error) {
	l.reads++
	if l.afterRead != nil {
		l.afterRead()
	}
	return l.l, true, l.err
}

func TestTrackingAccessLifecycleAndRedisDegradation(t *testing.T) {
	r := &trackingRepo{state: TrackingState{CourierID: 9, Version: 3, Status: domain.PickedUp}}
	now := time.Now().UTC()
	l := &liveLocations{l: domain.Location{CourierID: 9, Latitude: 40.28, Longitude: 69.62, AccuracyMeters: 12, RecordedAt: now, ReceivedAt: now}}
	s := NewTracking(r, l, time.Minute, 100)
	ctx := context.Background()
	for _, customer := range []int64{0, 8} {
		if _, err := s.Snapshot(ctx, customer, 42); err == nil {
			t.Fatal("foreign customer accepted")
		}
	}
	if _, err := s.Snapshot(ctx, 7, 43); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("pickup/nonexistent order exposed", err)
	}
	for _, status := range []string{domain.PendingAssignment, domain.Assigned, domain.Delivered, domain.Cancelled} {
		r.state.Status = status
		update, err := s.Snapshot(ctx, 7, 42)
		if err != nil || update.Location != nil || l.reads != 0 {
			t.Fatal(status, update, err)
		}
	}
	r.state.Status = domain.PickedUp
	update, err := s.Snapshot(ctx, 7, 42)
	if err != nil || update.Event != "location" || update.Location == nil {
		t.Fatal(update, err)
	}
	l.err = ErrLocationUnavailable
	update, err = s.Snapshot(ctx, 7, 42)
	if err != nil || update.Event != "location_unavailable" || update.Location != nil {
		t.Fatal(update, err)
	}
	l.err = nil
	l.l.RecordedAt = now.Add(-time.Minute)
	update, _ = s.Snapshot(ctx, 7, 42)
	if update.Location != nil {
		t.Fatal("expired location exposed")
	}
	l.l.RecordedAt = now
	l.afterRead = func() { r.state.CourierID = 10; r.state.Version++ }
	update, _ = s.Snapshot(ctx, 7, 42)
	if update.Location != nil {
		t.Fatal("old assignment exposed")
	}
	l.afterRead = func() { r.state.Status = domain.Delivered; r.state.Version++ }
	update, _ = s.Snapshot(ctx, 7, 42)
	if update.Event != "tracking_ended" || update.Location != nil {
		t.Fatal(update)
	}
}

func TestTrackingGPSUsesServerReceivedTimeAndValidatesBeforeIO(t *testing.T) {
	r := &trackingRepo{}
	l := &liveLocations{}
	s := NewTracking(r, l, time.Minute, 100)
	v := domain.Location{CourierID: 9, Latitude: 40, Longitude: 69, AccuracyMeters: 10, RecordedAt: time.Now(), ReceivedAt: time.Unix(1, 0)}
	if err := s.Update(context.Background(), v); err != nil || time.Since(l.l.ReceivedAt) > time.Second {
		t.Fatal(l.l, err)
	}
	v.Latitude = 100
	if err := s.Update(context.Background(), v); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	r.err = domain.ErrConflict
	v.Latitude = 40
	if err := s.Update(context.Background(), v); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
}
