package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNewRestaurant(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n", "Cafe\x00", strings.Repeat("я", 201)} {
		if _, err := NewRestaurant(name, true); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: expected invalid name, got %v", name, err)
		}
	}
	r, err := NewRestaurant("  Кафе  ", true)
	if err != nil || r.Name != "Кафе" || !r.IsOpen || r.ID != 0 || !r.CreatedAt.IsZero() {
		t.Fatalf("unexpected restaurant: %+v, %v", r, err)
	}
	if _, err := NewRestaurant(strings.Repeat("я", 200), false); err != nil {
		t.Fatal(err)
	}
}

func TestChanges(t *testing.T) {
	if _, err := (RestaurantChanges{}).Normalize(); !errors.Is(err, ErrEmptyUpdate) {
		t.Fatal(err)
	}
	name := "  Renamed  "
	closed := false
	c, err := (RestaurantChanges{Name: &name, IsOpen: &closed}).Normalize()
	if err != nil || *c.Name != "Renamed" || *c.IsOpen || name != "  Renamed  " {
		t.Fatalf("changes: %+v, %v", c, err)
	}
	name = "  "
	if _, err := (RestaurantChanges{Name: &name}).Normalize(); !errors.Is(err, ErrInvalidName) {
		t.Fatal(err)
	}
	if _, err := (RestaurantChanges{IsOpen: &closed}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateID(t *testing.T) {
	for _, id := range []int64{-1, 0} {
		if !errors.Is(ValidateID(id), ErrInvalidID) {
			t.Errorf("accepted id %d", id)
		}
	}
	if err := ValidateID(1); err != nil {
		t.Fatal(err)
	}
}

func TestRestaurantPickupAddress(t *testing.T) {
	for _, address := range []string{"", "  ", strings.Repeat("я", 501), "location\x00"} {
		if _, err := NormalizeAddress(address); !errors.Is(err, ErrInvalidAddress) {
			t.Fatal(err)
		}
	}
	address := "  Restaurant street 1  "
	changes, err := (RestaurantChanges{Address: &address}).Normalize()
	if err != nil || *changes.Address != "Restaurant street 1" {
		t.Fatal(changes, err)
	}
}
