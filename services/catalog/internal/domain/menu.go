package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const CurrencyTJS = "TJS"

var (
	ErrInvalidMenuID              = errors.New("category and item identifiers must be positive")
	ErrCategoryNotFound           = errors.New("menu category not found")
	ErrItemNotFound               = errors.New("menu item not found")
	ErrCategoryRestaurantMismatch = errors.New("category must belong to the item's restaurant")
	ErrInvalidPrice               = errors.New("price_minor must be greater than zero")
	ErrInvalidCurrency            = errors.New("currency must be TJS")
	ErrInvalidSortOrder           = errors.New("sort_order must be non-negative")
	ErrInvalidDescription         = errors.New("description must contain at most 2000 characters and no null characters")
	ErrEmptyMenuUpdate            = errors.New("provide at least one editable menu field")
)

type MenuCategory struct {
	ID           int64
	RestaurantID int64
	Name         string
	SortOrder    int32
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type MenuItem struct {
	ID           int64
	RestaurantID int64
	CategoryID   int64
	Name         string
	Description  string
	PriceMinor   int64
	Currency     string
	IsAvailable  bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type MenuCategoryChanges struct {
	Name      *string
	SortOrder *int32
}

type MenuItemChanges struct {
	CategoryID  *int64
	Name        *string
	Description *string
	PriceMinor  *int64
	Currency    *string
	IsAvailable *bool
}

func ValidateMenuID(id int64) error {
	if id <= 0 {
		return ErrInvalidMenuID
	}
	return nil
}

func ValidateCategoryRestaurant(restaurantID int64, category MenuCategory) error {
	if category.RestaurantID != restaurantID {
		return ErrCategoryRestaurantMismatch
	}
	return nil
}

func validateDescription(value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > 2000 {
		return ErrInvalidDescription
	}
	return nil
}

func NewMenuCategory(restaurantID int64, name string, sortOrder int32) (MenuCategory, error) {
	if err := ValidateID(restaurantID); err != nil {
		return MenuCategory{}, err
	}
	name, err := NormalizeName(name)
	if err != nil {
		return MenuCategory{}, err
	}
	if sortOrder < 0 {
		return MenuCategory{}, ErrInvalidSortOrder
	}
	return MenuCategory{RestaurantID: restaurantID, Name: name, SortOrder: sortOrder}, nil
}

func NewMenuItem(restaurantID, categoryID int64, name, description string, priceMinor int64, currency string, available bool) (MenuItem, error) {
	if err := ValidateID(restaurantID); err != nil {
		return MenuItem{}, err
	}
	if err := ValidateMenuID(categoryID); err != nil {
		return MenuItem{}, err
	}
	name, err := NormalizeName(name)
	if err != nil {
		return MenuItem{}, err
	}
	if err := validateDescription(description); err != nil {
		return MenuItem{}, err
	}
	if priceMinor <= 0 {
		return MenuItem{}, ErrInvalidPrice
	}
	if currency != CurrencyTJS {
		return MenuItem{}, ErrInvalidCurrency
	}
	return MenuItem{RestaurantID: restaurantID, CategoryID: categoryID, Name: name, Description: description, PriceMinor: priceMinor, Currency: currency, IsAvailable: available}, nil
}

func (c MenuCategoryChanges) Normalize() (MenuCategoryChanges, error) {
	if c.Name == nil && c.SortOrder == nil {
		return MenuCategoryChanges{}, ErrEmptyMenuUpdate
	}
	if c.Name != nil {
		name, err := NormalizeName(*c.Name)
		if err != nil {
			return MenuCategoryChanges{}, err
		}
		c.Name = &name
	}
	if c.SortOrder != nil && *c.SortOrder < 0 {
		return MenuCategoryChanges{}, ErrInvalidSortOrder
	}
	return c, nil
}

func (c MenuItemChanges) Normalize() (MenuItemChanges, error) {
	if c.CategoryID == nil && c.Name == nil && c.Description == nil && c.PriceMinor == nil && c.Currency == nil && c.IsAvailable == nil {
		return MenuItemChanges{}, ErrEmptyMenuUpdate
	}
	if c.CategoryID != nil {
		if err := ValidateMenuID(*c.CategoryID); err != nil {
			return MenuItemChanges{}, err
		}
	}
	if c.Name != nil {
		name, err := NormalizeName(*c.Name)
		if err != nil {
			return MenuItemChanges{}, err
		}
		c.Name = &name
	}
	if c.Description != nil {
		if err := validateDescription(*c.Description); err != nil {
			return MenuItemChanges{}, err
		}
	}
	if c.PriceMinor != nil && *c.PriceMinor <= 0 {
		return MenuItemChanges{}, ErrInvalidPrice
	}
	if c.Currency != nil && *c.Currency != CurrencyTJS {
		return MenuItemChanges{}, ErrInvalidCurrency
	}
	return c, nil
}
