package geo

import (
	"encoding/json"
	"math"
	"testing"
)

func TestRangesAndProjection(t *testing.T) {
	for _, p := range []Point{{0, 0}, {90, 180}, {-90, -180}, {38.56, 68.77}} {
		if !p.Valid() {
			t.Fatal(p)
		}
	}
	for _, p := range []Point{{91, 0}, {0, 181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if p.Valid() {
			t.Fatal(p)
		}
	}
	if (Point{90, 0}).Searchable() {
		t.Fatal("polar coordinates accepted by Redis projection")
	}
}

func TestJSONRequiresBothCoordinates(t *testing.T) {
	for _, body := range []string{`{}`, `{"latitude":0}`, `{"latitude":null,"longitude":0}`, `{"latitude":91,"longitude":0}`} {
		var p Point
		if err := json.Unmarshal([]byte(body), &p); err == nil {
			t.Fatal(body)
		}
	}
	var p Point
	if err := json.Unmarshal([]byte(`{"latitude":0,"longitude":0}`), &p); err != nil || !p.Valid() {
		t.Fatal(p, err)
	}
}
