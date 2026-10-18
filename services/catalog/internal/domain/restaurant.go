package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Desu-php/food/contracts/geo"
)

var (
	ErrCoordinates    = errors.New("provide both latitude [-90,90] and longitude [-180,180]")
	ErrInvalidAddress = errors.New("address must contain 1 to 500 characters")
	ErrInvalidName    = errors.New("name must contain 1 to 200 characters")
	ErrInvalidID      = errors.New("restaurant id must be positive")
	ErrNotFound       = errors.New("restaurant not found")
	ErrEmptyUpdate    = errors.New("provide name, address, is_open or coordinates to update")
)

// Restaurant is a catalog entity. IDs and timestamps are assigned by persistence.
type Restaurant struct {
	Latitude  *float64
	Longitude *float64
	ID        int64
	Name      string
	Address   string
	IsOpen    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RestaurantChanges struct {
	Latitude  *float64
	Longitude *float64
	Name      *string
	Address   *string
	IsOpen    *bool
}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || name == "" || utf8.RuneCountInString(name) > 200 {
		return "", ErrInvalidName
	}
	return name, nil
}

func NewRestaurant(name string, isOpen bool) (Restaurant, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Restaurant{}, err
	}
	return Restaurant{Name: name, IsOpen: isOpen}, nil
}

func ValidateID(id int64) error {
	if id <= 0 {
		return ErrInvalidID
	}
	return nil
}

func (c RestaurantChanges) Normalize() (RestaurantChanges, error) {
	if c.Name == nil && c.IsOpen == nil && c.Address == nil && c.Latitude == nil && c.Longitude == nil {
		return RestaurantChanges{}, ErrEmptyUpdate
	}
	if (c.Latitude == nil) != (c.Longitude == nil) || c.Latitude != nil && !(geo.Point{Latitude: *c.Latitude, Longitude: *c.Longitude}).Valid() {
		return RestaurantChanges{}, ErrCoordinates
	}
	if c.Address != nil {
		address, err := NormalizeAddress(*c.Address)
		if err != nil {
			return RestaurantChanges{}, err
		}
		c.Address = &address
	}
	if c.Name != nil {
		name, err := NormalizeName(*c.Name)
		if err != nil {
			return RestaurantChanges{}, err
		}
		c.Name = &name
	}
	return c, nil
}

func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || strings.ContainsRune(s, 0) || utf8.RuneCountInString(s) > 500 {
		return "", ErrInvalidAddress
	}
	return s, nil
}
