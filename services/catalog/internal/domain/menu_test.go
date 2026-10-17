package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestMenuCategoryRules(t *testing.T) {
	c, err := NewMenuCategory(1, " Пицца ", 0)
	if err != nil || c.Name != "Пицца" || c.RestaurantID != 1 || c.SortOrder != 0 || c.ID != 0 {
		t.Fatalf("category: %+v %v", c, err)
	}
	for _, tt := range []struct {
		name         string
		restaurantID int64
		order        int32
		want         error
	}{
		{"", 1, 0, ErrInvalidName}, {" ", 1, 0, ErrInvalidName}, {strings.Repeat("я", 201), 1, 0, ErrInvalidName},
		{"Cafe\x00", 1, 0, ErrInvalidName}, {"Cafe", 0, 0, ErrInvalidID}, {"Cafe", 1, -1, ErrInvalidSortOrder},
	} {
		if _, err := NewMenuCategory(tt.restaurantID, tt.name, tt.order); !errors.Is(err, tt.want) {
			t.Errorf("category %q: %v, want %v", tt.name, err, tt.want)
		}
	}
	if err := ValidateCategoryRestaurant(2, c); !errors.Is(err, ErrCategoryRestaurantMismatch) {
		t.Fatal(err)
	}
	if err := ValidateCategoryRestaurant(1, c); err != nil {
		t.Fatal(err)
	}
}

func TestMenuItemRules(t *testing.T) {
	i, err := NewMenuItem(1, 2, " Маргарита ", "Сыр", 6500, CurrencyTJS, false)
	if err != nil || i.Name != "Маргарита" || i.PriceMinor != 6500 || i.IsAvailable || i.Currency != CurrencyTJS {
		t.Fatalf("item: %+v %v", i, err)
	}
	tests := []struct {
		name, description, currency string
		price, category, restaurant int64
		want                        error
	}{
		{"", "", "TJS", 1, 2, 1, ErrInvalidName}, {" ", "", "TJS", 1, 2, 1, ErrInvalidName},
		{"Item", "", "TJS", 0, 2, 1, ErrInvalidPrice}, {"Item", "", "TJS", -1, 2, 1, ErrInvalidPrice},
		{"Item", "", "USD", 1, 2, 1, ErrInvalidCurrency}, {"Item", "", "", 1, 2, 1, ErrInvalidCurrency},
		{"Item", "", "TJS", 1, 0, 1, ErrInvalidMenuID}, {"Item", "", "TJS", 1, 2, 0, ErrInvalidID},
		{"Item", strings.Repeat("я", 2001), "TJS", 1, 2, 1, ErrInvalidDescription},
		{"Item", "NUL\x00", "TJS", 1, 2, 1, ErrInvalidDescription},
	}
	for _, tt := range tests {
		if _, err := NewMenuItem(tt.restaurant, tt.category, tt.name, tt.description, tt.price, tt.currency, true); !errors.Is(err, tt.want) {
			t.Errorf("item %+v: %v", tt, err)
		}
	}
	if _, err := NewMenuItem(1, 2, "Item", "", math.MaxInt64, CurrencyTJS, true); err != nil {
		t.Fatal(err)
	}
}

func TestMenuPartialChanges(t *testing.T) {
	zero := int32(0)
	closed := false
	empty := ""
	category, err := (MenuCategoryChanges{SortOrder: &zero}).Normalize()
	if err != nil || category.Name != nil || category.SortOrder == nil || *category.SortOrder != 0 {
		t.Fatalf("zero order lost: %+v %v", category, err)
	}
	item, err := (MenuItemChanges{IsAvailable: &closed, Description: &empty}).Normalize()
	if err != nil || item.IsAvailable == nil || *item.IsAvailable || item.Description == nil || *item.Description != "" || item.PriceMinor != nil {
		t.Fatalf("zero fields lost: %+v %v", item, err)
	}
	if _, err := (MenuCategoryChanges{}).Normalize(); !errors.Is(err, ErrEmptyMenuUpdate) {
		t.Fatal(err)
	}
	if _, err := (MenuItemChanges{}).Normalize(); !errors.Is(err, ErrEmptyMenuUpdate) {
		t.Fatal(err)
	}
	price := int64(0)
	if _, err := (MenuItemChanges{PriceMinor: &price}).Normalize(); !errors.Is(err, ErrInvalidPrice) {
		t.Fatal(err)
	}
	if _, err := (MenuItemChanges{Name: &empty}).Normalize(); !errors.Is(err, ErrInvalidName) {
		t.Fatal(err)
	}
	if _, err := (MenuCategoryChanges{Name: &empty}).Normalize(); !errors.Is(err, ErrInvalidName) {
		t.Fatal(err)
	}
	invalidCurrency := "USD"
	if _, err := (MenuItemChanges{Currency: &invalidCurrency}).Normalize(); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatal(err)
	}
	if _, err := (MenuItemChanges{CategoryID: &price}).Normalize(); !errors.Is(err, ErrInvalidMenuID) {
		t.Fatal(err)
	}
	negative := int32(-1)
	if _, err := (MenuCategoryChanges{SortOrder: &negative}).Normalize(); !errors.Is(err, ErrInvalidSortOrder) {
		t.Fatal(err)
	}
	name := " Changed "
	normalized, err := (MenuItemChanges{Name: &name}).Normalize()
	if err != nil || *normalized.Name != "Changed" || name != " Changed " {
		t.Fatal("normalization mutated caller data")
	}
}
