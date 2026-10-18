package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/food/contracts/geo"
	"example.com/food/delivery/internal/domain"
)

type dispatchRepo struct {
	assignErr error
	DispatchRepository
	claim        DispatchClaim
	found        bool
	finishReason string
	stop         bool
	attempted    bool
	result       bool
}

func (r *dispatchRepo) ClaimDispatch(context.Context, DispatchConfig) (DispatchClaim, bool, error) {
	return r.claim, r.found, nil
}
func (r *dispatchRepo) FinishDispatch(_ context.Context, _ DispatchClaim, _ DispatchConfig, reason string, stop bool) error {
	r.finishReason = reason
	r.stop = stop
	return nil
}
func (r *dispatchRepo) AssignDispatch(context.Context, DispatchClaim, []Candidate, time.Duration) (bool, error) {
	r.attempted = true
	return r.result, r.assignErr
}

type dispatchLocations struct {
	Locations
	calls      int
	err        error
	candidates []Candidate
}

func (l *dispatchLocations) Nearest(context.Context, geo.Point, float64) ([]Candidate, error) {
	l.calls++
	return l.candidates, l.err
}
func TestDispatchDegradationAndHistoricalSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location *geo.Point
		deadline bool
		redisErr error
		want     string
		stop     bool
	}{
		{"no_coordinates", nil, true, nil, "restaurant_coordinates_missing", true},
		{"historical_no_deadline", &geo.Point{}, false, nil, "deadline_expired", true},
		{"polar_restaurant", &geo.Point{Latitude: 90}, true, nil, "restaurant_outside_geo_projection", true},
		{"no_candidates", &geo.Point{}, true, nil, "no_available_courier", false},
		{"redis_down", &geo.Point{}, true, ErrLocationUnavailable, "redis_unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := domain.Delivery{RestaurantLocation: tc.location}
			if tc.deadline {
				deadline := time.Now().Add(time.Minute)
				d.DispatchDeadline = &deadline
			}
			r := &dispatchRepo{found: true, claim: DispatchClaim{Delivery: d}}
			l := &dispatchLocations{err: tc.redisErr}
			worked, err := NewDispatch(r, l, DefaultDispatchConfig()).Step(context.Background())
			if err != nil || !worked || r.finishReason != tc.want || r.stop != tc.stop {
				t.Fatal(worked, err, r)
			}
			if tc.stop && l.calls != 0 {
				t.Fatal("queried Redis for ineligible delivery")
			}
		})
	}
}
func TestDispatchEmptyAndSuccessfulWork(t *testing.T) {
	r := &dispatchRepo{}
	l := &dispatchLocations{}
	worker := NewDispatch(r, l, DefaultDispatchConfig())
	if found, err := worker.Step(context.Background()); err != nil || found || l.calls != 0 {
		t.Fatal(found, err)
	}
	deadline := time.Now().Add(time.Minute)
	r.found = true
	r.result = true
	r.claim.Delivery = domain.Delivery{RestaurantLocation: &geo.Point{}, DispatchDeadline: &deadline}
	if worked, err := worker.Step(context.Background()); err != nil || !worked || !r.attempted || r.finishReason != "" {
		t.Fatal(worked, err, r)
	}
}
func TestDispatchLocationValidationBeforeIO(t *testing.T) {
	worker := NewDispatch(nil, nil, DefaultDispatchConfig())
	if err := worker.UpdateLocation(context.Background(), 0, geo.Point{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal(err)
	}
	if err := worker.UpdateLocation(context.Background(), 1, geo.Point{Latitude: 91}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestDispatchRecordsAssignmentFailure(t *testing.T) {
	failure := errors.New("outbox write failed")
	deadline := time.Now().Add(time.Minute)
	r := &dispatchRepo{found: true, assignErr: failure, claim: DispatchClaim{Delivery: domain.Delivery{RestaurantLocation: &geo.Point{}, DispatchDeadline: &deadline}}}
	worker := NewDispatch(r, &dispatchLocations{}, DefaultDispatchConfig())
	if found, err := worker.Step(context.Background()); !found || !errors.Is(err, failure) || r.finishReason != "assignment_failed" {
		t.Fatal(found, err, r.finishReason)
	}
}
