package domain

import (
	"errors"
	"math"
	"testing"

	"github.com/Desu-php/food/contracts/geo"
)

func TestDistanceMeters(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b geo.Point
		want int64
	}{
		{"same", geo.Point{Latitude: 38.56, Longitude: 68.77}, geo.Point{Latitude: 38.56, Longitude: 68.77}, 0},
		{"equator degree", geo.Point{}, geo.Point{Longitude: 1}, 111196},
		{"date line", geo.Point{Longitude: 179.5}, geo.Point{Longitude: -179.5}, 111196},
		{"antipodal", geo.Point{}, geo.Point{Longitude: 180}, 20015115},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DistanceMeters(tc.a, tc.b)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
			reverse, err := DistanceMeters(tc.b, tc.a)
			if err != nil || reverse != got {
				t.Fatal(reverse, err)
			}
		})
	}
	for _, p := range []geo.Point{{Latitude: 91}, {Longitude: -181}, {Latitude: math.NaN()}, {Longitude: math.Inf(1)}} {
		if _, err := DistanceMeters(p, geo.Point{}); !errors.Is(err, ErrCoordinates) {
			t.Fatal(p, err)
		}
	}
}
func TestTariffBoundaries(t *testing.T) {
	tiers := []DeliveryTariff{{ID: 3, MaxDistanceMeters: 8000, DeliveryFeeMinor: 2500}, {ID: 1, MaxDistanceMeters: 2000, DeliveryFeeMinor: 0}, {ID: 2, MaxDistanceMeters: 5000, DeliveryFeeMinor: 1500}}
	for _, tc := range []struct{ d, id int64 }{{0, 1}, {2000, 1}, {2001, 2}, {5000, 2}, {5001, 3}, {8000, 3}, {8001, 0}, {-1, 0}} {
		got, ok := SelectTariff(tiers, tc.d)
		if ok != (tc.id != 0) || got.ID != tc.id {
			t.Fatal(tc, got, ok)
		}
	}
	if tiers[0].ID != 3 {
		t.Fatal("selection mutated source")
	}
	for _, tc := range []DeliveryTariff{{MaxDistanceMeters: 0}, {MaxDistanceMeters: 1, DeliveryFeeMinor: -1}} {
		if !errors.Is(tc.Validate(), ErrTariff) {
			t.Fatal(tc)
		}
	}
	if _, err := AddMinor(math.MaxInt64, 1); !errors.Is(err, ErrAmountOverflow) {
		t.Fatal(err)
	}
}
