package application

import (
	"context"
	"errors"
	"fmt"

	"example.com/food/catalog/internal/domain"
)

var ErrInvalidPagination = errors.New("limit must be between 1 and 100; offset must be non-negative")

type Page struct {
	Limit  int
	Offset int
}

// RestaurantRepository is the persistence port required by the use cases.
type RestaurantRepository interface {
	List(context.Context, Page) ([]domain.Restaurant, error)
	Get(context.Context, int64) (domain.Restaurant, error)
	Create(context.Context, domain.Restaurant) (domain.Restaurant, error)
	Update(context.Context, int64, domain.RestaurantChanges) (domain.Restaurant, error)
}

type Restaurants struct{ repo RestaurantRepository }

func NewRestaurants(repo RestaurantRepository) *Restaurants { return &Restaurants{repo: repo} }

func (s *Restaurants) List(ctx context.Context, page Page) ([]domain.Restaurant, error) {
	if page.Limit < 1 || page.Limit > 100 || page.Offset < 0 {
		return nil, ErrInvalidPagination
	}
	restaurants, err := s.repo.List(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("list restaurants: %w", err)
	}
	return restaurants, nil
}

func (s *Restaurants) Get(ctx context.Context, id int64) (domain.Restaurant, error) {
	if err := domain.ValidateID(id); err != nil {
		return domain.Restaurant{}, err
	}
	restaurant, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("get restaurant: %w", err)
	}
	return restaurant, nil
}

func (s *Restaurants) Create(ctx context.Context, name string, isOpen bool, address ...string) (domain.Restaurant, error) {
	restaurant, err := domain.NewRestaurant(name, isOpen)
	if err != nil {
		return domain.Restaurant{}, err
	}
	if len(address) > 0 && address[0] != "" {
		restaurant.Address, err = domain.NormalizeAddress(address[0])
		if err != nil {
			return domain.Restaurant{}, err
		}
	}
	restaurant, err = s.repo.Create(ctx, restaurant)
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("create restaurant: %w", err)
	}
	return restaurant, nil
}

func (s *Restaurants) Update(ctx context.Context, id int64, changes domain.RestaurantChanges) (domain.Restaurant, error) {
	if err := domain.ValidateID(id); err != nil {
		return domain.Restaurant{}, err
	}
	changes, err := changes.Normalize()
	if err != nil {
		return domain.Restaurant{}, err
	}
	restaurant, err := s.repo.Update(ctx, id, changes)
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("update restaurant: %w", err)
	}
	return restaurant, nil
}
