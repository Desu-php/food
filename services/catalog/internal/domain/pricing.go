package domain

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/Desu-php/food/contracts/geo"
)

var (
	ErrTariff          = errors.New("tariff requires a positive radius in meters and nonnegative fee in minor units")
	ErrTariffConflict  = errors.New("a tariff with this maximum distance already exists")
	ErrTariffNotFound  = errors.New("delivery tariff not found")
	ErrMinimumSubtotal = errors.New("minimum delivery subtotal must be nonnegative")
	ErrAmountOverflow  = errors.New("quote amount exceeds int64 range")
)

type DeliveryTariff struct {
	ID                int64     `json:"id"`
	RestaurantID      int64     `json:"restaurant_id"`
	MaxDistanceMeters int64     `json:"max_distance_meters"`
	DeliveryFeeMinor  int64     `json:"delivery_fee_minor"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (t DeliveryTariff) Validate() error {
	if t.MaxDistanceMeters <= 0 || t.DeliveryFeeMinor < 0 {
		return ErrTariff
	}
	return nil
}

type DeliverySettings struct {
	DeliveryEnabled              bool  `json:"delivery_enabled"`
	PickupEnabled                bool  `json:"pickup_enabled"`
	MinimumDeliverySubtotalMinor int64 `json:"minimum_delivery_subtotal_minor"`
	Version                      int64 `json:"version"`
}
type PricingSnapshot struct {
	SettingsVersion              int64  `json:"settings_version"`
	MinimumDeliverySubtotalMinor int64  `json:"minimum_delivery_subtotal_minor"`
	TariffID                     int64  `json:"tariff_id,omitempty"`
	MaxDistanceMeters            int64  `json:"max_distance_meters,omitempty"`
	DeliveryFeeMinor             int64  `json:"delivery_fee_minor"`
	DistanceMeters               *int64 `json:"distance_meters,omitempty"`
}

// DistanceMeters uses a mean spherical Earth radius and rounds upward once.
// The result is a straight-line distance, never a road distance or ETA.
func DistanceMeters(a, b geo.Point) (int64, error) {
	if !a.Valid() || !b.Valid() {
		return 0, ErrCoordinates
	}
	const radians = math.Pi / 180
	dlat := (b.Latitude - a.Latitude) * radians
	dlon := (b.Longitude - a.Longitude) * radians
	h := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(a.Latitude*radians)*math.Cos(b.Latitude*radians)*math.Sin(dlon/2)*math.Sin(dlon/2)
	h = math.Max(0, math.Min(1, h))
	return int64(math.Ceil(6371008.8 * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h)))), nil
}

func SelectTariff(tariffs []DeliveryTariff, distance int64) (DeliveryTariff, bool) {
	if distance < 0 {
		return DeliveryTariff{}, false
	}
	sorted := slices.Clone(tariffs)
	slices.SortFunc(sorted, func(a, b DeliveryTariff) int {
		if a.MaxDistanceMeters < b.MaxDistanceMeters {
			return -1
		}
		if a.MaxDistanceMeters > b.MaxDistanceMeters {
			return 1
		}
		return 0
	})
	for _, t := range sorted {
		if distance <= t.MaxDistanceMeters {
			return t, true
		}
	}
	return DeliveryTariff{}, false
}
func AddMinor(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, ErrAmountOverflow
	}
	return a + b, nil
}
