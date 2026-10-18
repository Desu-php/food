package domain

import (
	"errors"
	"math"
	"time"

	"github.com/Desu-php/food/contracts/geo"
)

var ErrStaleLocation = errors.New("location update is not newer")

const (
	LocationClockSkew         = 5 * time.Second
	MaxLocationAccuracyMeters = 1000
)

// Location is the latest observation, not a movement history or a route estimate.
type Location struct {
	CourierID      int64     `json:"courier_id"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	AccuracyMeters float64   `json:"accuracy_meters"`
	RecordedAt     time.Time `json:"recorded_at"`
	ReceivedAt     time.Time `json:"received_at"`
}

func (l Location) Validate(now time.Time, ttl time.Duration, maxAccuracy float64) error {
	if l.CourierID <= 0 || !(geo.Point{Latitude: l.Latitude, Longitude: l.Longitude}).Valid() ||
		math.IsNaN(l.AccuracyMeters) || math.IsInf(l.AccuracyMeters, 0) || l.AccuracyMeters <= 0 || l.AccuracyMeters > maxAccuracy ||
		l.RecordedAt.IsZero() || l.RecordedAt.After(now.Add(LocationClockSkew)) || !l.RecordedAt.After(now.Add(-ttl)) {
		return ErrInvalid
	}
	return nil
}

func (l Location) Fresh(now time.Time, ttl time.Duration) bool {
	return !l.ReceivedAt.IsZero() && l.ReceivedAt.After(now.Add(-ttl)) && l.RecordedAt.After(now.Add(-ttl)) && !l.RecordedAt.After(now.Add(LocationClockSkew))
}
