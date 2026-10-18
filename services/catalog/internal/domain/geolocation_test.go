package domain

import (
	"math"
	"testing"
)

func TestCoordinateChangesArePairedAndValidated(t *testing.T) {
	for _, tc := range []struct {
		lat, lon float64
		valid    bool
	}{{0, 0, true}, {90, -180, true}, {-90, 180, true}, {91, 0, false}, {0, 181, false}, {math.NaN(), 0, false}, {0, math.Inf(1), false}} {
		_, err := (RestaurantChanges{Latitude: &tc.lat, Longitude: &tc.lon}).Normalize()
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	n := 1.0
	if _, err := (RestaurantChanges{Latitude: &n}).Normalize(); err == nil {
		t.Fatal("unpaired coordinates accepted")
	}
}
