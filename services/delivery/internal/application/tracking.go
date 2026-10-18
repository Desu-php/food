package application

import (
	"context"
	"errors"
	"time"

	"github.com/Desu-php/food/services/delivery/internal/domain"
)

// TrackingState is a customer-scoped PostgreSQL snapshot. PICKUP has no delivery row.
type TrackingState struct {
	CourierID int64
	Version   int64
	Status    string
}

type TrackingRepository interface {
	TrackingState(context.Context, int64, int64) (TrackingState, error)
	CourierLocationAllowed(context.Context, int64) error
}

type LiveLocations interface {
	Store(context.Context, domain.Location) error
	Latest(context.Context, int64) (domain.Location, bool, error)
}

type Tracking struct {
	repo        TrackingRepository
	locations   LiveLocations
	ttl         time.Duration
	maxAccuracy float64
}

func NewTracking(repo TrackingRepository, locations LiveLocations, ttl time.Duration, maxAccuracy float64) *Tracking {
	return &Tracking{repo: repo, locations: locations, ttl: ttl, maxAccuracy: maxAccuracy}
}

func (s *Tracking) Update(ctx context.Context, l domain.Location) error {
	l.RecordedAt = l.RecordedAt.UTC().Truncate(time.Microsecond)
	l.ReceivedAt = time.Now().UTC()
	if err := l.Validate(l.ReceivedAt, s.ttl, s.maxAccuracy); err != nil {
		return err
	}
	if err := s.repo.CourierLocationAllowed(ctx, l.CourierID); err != nil {
		return err
	}
	return s.locations.Store(ctx, l)
}

type TrackingUpdate struct {
	Event    string
	Status   string
	Location *domain.Location
}

func (s *Tracking) Snapshot(ctx context.Context, customer, order int64) (TrackingUpdate, error) {
	if customer <= 0 {
		return TrackingUpdate{}, domain.ErrForbidden
	}
	if order <= 0 {
		return TrackingUpdate{}, domain.ErrInvalid
	}
	state, err := s.repo.TrackingState(ctx, customer, order)
	if err != nil {
		return TrackingUpdate{}, err
	}
	update := TrackingUpdate{Status: state.Status}
	if state.Status == domain.Delivered || state.Status == domain.Cancelled {
		update.Event = "tracking_ended"
		return update, nil
	}
	if state.Status != domain.PickedUp || state.CourierID <= 0 {
		update.Event = "tracking_pending"
		return update, nil
	}
	l, found, err := s.locations.Latest(ctx, state.CourierID)
	if err != nil && !errors.Is(err, ErrLocationUnavailable) {
		return TrackingUpdate{}, err
	}
	// Recheck after Redis I/O: never emit a snapshot from a previous assignment or
	// one whose delivery completed while the location read was in flight.
	current, checkErr := s.repo.TrackingState(ctx, customer, order)
	if checkErr != nil {
		return TrackingUpdate{}, checkErr
	}
	if current != state {
		if current.Status == domain.Delivered || current.Status == domain.Cancelled {
			return TrackingUpdate{Event: "tracking_ended", Status: current.Status}, nil
		}
		return TrackingUpdate{Event: "location_unavailable", Status: current.Status}, nil
	}
	update.Event = "location_unavailable"
	if err == nil && found && l.CourierID == state.CourierID && l.Fresh(time.Now(), s.ttl) && l.Validate(time.Now(), s.ttl, s.maxAccuracy) == nil {
		update.Event = "location"
		update.Location = &l
	}
	return update, nil
}

func (s *Delivery) WithTracking(tracking *Tracking) *Delivery { s.tracking = tracking; return s }
func (s *Delivery) UpdateCourierLocation(ctx context.Context, l domain.Location) error {
	if s.tracking == nil {
		return ErrLocationUnavailable
	}
	return s.tracking.Update(ctx, l)
}
func (s *Delivery) Track(ctx context.Context, customer, order int64) (TrackingUpdate, error) {
	if s.tracking == nil {
		return TrackingUpdate{}, ErrLocationUnavailable
	}
	return s.tracking.Snapshot(ctx, customer, order)
}
