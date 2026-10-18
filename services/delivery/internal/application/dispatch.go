package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"example.com/food/contracts/geo"
	"example.com/food/delivery/internal/domain"
)

var (
	ErrLocationRate        = errors.New("location update rate exceeded")
	ErrLocationUnavailable = errors.New("geolocation temporarily unavailable")
)

type Candidate struct {
	CourierID      int64
	DistanceMeters float64
	ExpiresAt      time.Time
}

type Locations interface {
	Update(context.Context, int64, geo.Point) error
	Nearest(context.Context, geo.Point, float64) ([]Candidate, error)
}

type DispatchClaim struct {
	Delivery domain.Delivery
	Token    string
}

type DispatchConfig struct {
	PollInterval, Lease, RetryInterval, Cooldown, OperationTimeout time.Duration
	RadiusMeters                                                   float64
	MaxAttempts                                                    int
}

func DefaultDispatchConfig() DispatchConfig {
	return DispatchConfig{PollInterval: time.Second, Lease: 15 * time.Second, RetryInterval: 5 * time.Second, Cooldown: 2 * time.Minute, OperationTimeout: 3 * time.Second, RadiusMeters: 5000, MaxAttempts: 60}
}

type DispatchRepository interface {
	ClaimDispatch(context.Context, DispatchConfig) (DispatchClaim, bool, error)
	AssignDispatch(context.Context, DispatchClaim, []Candidate, time.Duration) (bool, error)
	FinishDispatch(context.Context, DispatchClaim, DispatchConfig, string, bool) error
	RetryDispatch(context.Context, int64) error
	CourierAvailable(context.Context, int64) error
}

type Dispatch struct {
	repo      DispatchRepository
	locations Locations
	cfg       DispatchConfig
}

func NewDispatch(repo DispatchRepository, locations Locations, cfg DispatchConfig) *Dispatch {
	return &Dispatch{repo, locations, cfg}
}

func (s *Dispatch) UpdateLocation(ctx context.Context, courier int64, p geo.Point) error {
	if courier <= 0 {
		return domain.ErrForbidden
	}
	if !p.Searchable() {
		return domain.ErrInvalid
	}
	if err := s.repo.CourierAvailable(ctx, courier); err != nil {
		return err
	}
	return s.locations.Update(ctx, courier, p)
}
func (s *Dispatch) Retry(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return s.repo.RetryDispatch(ctx, id)
}

// Step owns no SQL transaction while it waits for the ephemeral location index.
func (s *Dispatch) Step(ctx context.Context) (bool, error) {
	claim, found, err := s.repo.ClaimDispatch(ctx, s.cfg)
	if err != nil || !found {
		return found, err
	}
	d := claim.Delivery
	reason := "no_available_courier"
	stop := false
	if d.RestaurantLocation == nil {
		reason = "restaurant_coordinates_missing"
		stop = true
	} else if !d.RestaurantLocation.Searchable() {
		reason = "restaurant_outside_geo_projection"
		stop = true
	} else if d.DispatchDeadline == nil || !time.Now().Before(*d.DispatchDeadline) {
		reason = "deadline_expired"
		stop = true
	} else {
		candidates, searchErr := s.locations.Nearest(ctx, *d.RestaurantLocation, s.cfg.RadiusMeters)
		if searchErr != nil {
			reason = "redis_unavailable"
		} else {
			assigned, assignErr := s.repo.AssignDispatch(ctx, claim, candidates, s.cfg.Cooldown)
			if assignErr != nil {
				finishErr := s.repo.FinishDispatch(ctx, claim, s.cfg, "assignment_failed", false)
				return true, errors.Join(assignErr, finishErr)
			}
			if assigned {
				return true, nil
			}
		}
	}
	return true, s.repo.FinishDispatch(ctx, claim, s.cfg, reason, stop)
}
func (s *Dispatch) Run(ctx context.Context, logger *slog.Logger) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		stepCtx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
		worked, err := s.Step(stepCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("automatic dispatch step failed", "error_type", "storage_or_internal")
		}
		delay := s.cfg.PollInterval
		if worked && err == nil {
			delay = 10 * time.Millisecond
		}
		timer.Reset(delay)
	}
}
