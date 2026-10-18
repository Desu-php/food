// Package geo defines the optional geographic snapshot shared by service contracts.
package geo

import (
	"encoding/json"
	"errors"
	"math"
)

type Point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (p Point) Valid() bool {
	return !math.IsNaN(p.Latitude) && !math.IsNaN(p.Longitude) && p.Latitude >= -90 && p.Latitude <= 90 && p.Longitude >= -180 && p.Longitude <= 180
}

// Searchable reflects the latitude limit of the Redis GEO projection.
func (p Point) Searchable() bool {
	return p.Valid() && p.Latitude >= -85.05112878 && p.Latitude <= 85.05112878
}
func Clone(p *Point) *Point {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func Equal(a, b *Point) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (p *Point) UnmarshalJSON(body []byte) error {
	var v struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return err
	}
	if v.Latitude == nil || v.Longitude == nil {
		return errors.New("both coordinates are required")
	}
	value := Point{Latitude: *v.Latitude, Longitude: *v.Longitude}
	if !value.Valid() {
		return errors.New("coordinates outside geographic range")
	}
	*p = value
	return nil
}
