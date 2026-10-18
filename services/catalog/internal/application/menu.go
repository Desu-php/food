package application

import (
	"context"
	"fmt"

	"github.com/Desu-php/food/services/catalog/internal/domain"
)

// RestaurantMenu is an application read model, independent of HTTP and SQL.
type RestaurantMenu struct {
	RestaurantID int64
	Categories   []MenuCategoryGroup
}

type MenuCategoryGroup struct {
	Category domain.MenuCategory
	Items    []domain.MenuItem
}

// MenuRepository is a focused persistence port for the menu module.
type MenuRepository interface {
	GetCategory(context.Context, int64) (domain.MenuCategory, error)
	CreateCategory(context.Context, domain.MenuCategory) (domain.MenuCategory, error)
	UpdateCategory(context.Context, int64, int64, domain.MenuCategoryChanges) (domain.MenuCategory, error)
	CreateItem(context.Context, domain.MenuItem) (domain.MenuItem, error)
	UpdateItem(context.Context, int64, int64, domain.MenuItemChanges) (domain.MenuItem, error)
	DeleteItem(context.Context, int64, int64) error
	GetMenu(context.Context, int64) (RestaurantMenu, error)
}

type Menu struct {
	restaurants RestaurantRepository
	repo        MenuRepository
}

func NewMenu(restaurants RestaurantRepository, repo MenuRepository) *Menu {
	return &Menu{restaurants: restaurants, repo: repo}
}

func (s *Menu) requireRestaurant(ctx context.Context, id int64) error {
	if err := domain.ValidateID(id); err != nil {
		return err
	}
	if _, err := s.restaurants.Get(ctx, id); err != nil {
		return fmt.Errorf("find menu restaurant: %w", err)
	}
	return nil
}

func (s *Menu) requireCategory(ctx context.Context, restaurantID, categoryID int64) error {
	category, err := s.repo.GetCategory(ctx, categoryID)
	if err != nil {
		return fmt.Errorf("find item category: %w", err)
	}
	return domain.ValidateCategoryRestaurant(restaurantID, category)
}

func (s *Menu) CreateMenuCategory(ctx context.Context, restaurantID int64, name string, sortOrder int32) (domain.MenuCategory, error) {
	category, err := domain.NewMenuCategory(restaurantID, name, sortOrder)
	if err != nil {
		return domain.MenuCategory{}, err
	}
	if err := s.requireRestaurant(ctx, restaurantID); err != nil {
		return domain.MenuCategory{}, err
	}
	category, err = s.repo.CreateCategory(ctx, category)
	if err != nil {
		return domain.MenuCategory{}, fmt.Errorf("create menu category: %w", err)
	}
	return category, nil
}

func (s *Menu) UpdateMenuCategory(ctx context.Context, restaurantID, categoryID int64, changes domain.MenuCategoryChanges) (domain.MenuCategory, error) {
	if err := domain.ValidateMenuID(categoryID); err != nil {
		return domain.MenuCategory{}, err
	}
	changes, err := changes.Normalize()
	if err != nil {
		return domain.MenuCategory{}, err
	}
	if err := s.requireRestaurant(ctx, restaurantID); err != nil {
		return domain.MenuCategory{}, err
	}
	category, err := s.repo.UpdateCategory(ctx, restaurantID, categoryID, changes)
	if err != nil {
		return domain.MenuCategory{}, fmt.Errorf("update menu category: %w", err)
	}
	return category, nil
}

func (s *Menu) CreateMenuItem(ctx context.Context, restaurantID, categoryID int64, name, description string, priceMinor int64, currency string, available bool) (domain.MenuItem, error) {
	item, err := domain.NewMenuItem(restaurantID, categoryID, name, description, priceMinor, currency, available)
	if err != nil {
		return domain.MenuItem{}, err
	}
	if err := s.requireRestaurant(ctx, restaurantID); err != nil {
		return domain.MenuItem{}, err
	}
	if err := s.requireCategory(ctx, restaurantID, categoryID); err != nil {
		return domain.MenuItem{}, err
	}
	item, err = s.repo.CreateItem(ctx, item)
	if err != nil {
		return domain.MenuItem{}, fmt.Errorf("create menu item: %w", err)
	}
	return item, nil
}

func (s *Menu) UpdateMenuItem(ctx context.Context, restaurantID, itemID int64, changes domain.MenuItemChanges) (domain.MenuItem, error) {
	if err := domain.ValidateMenuID(itemID); err != nil {
		return domain.MenuItem{}, err
	}
	changes, err := changes.Normalize()
	if err != nil {
		return domain.MenuItem{}, err
	}
	if err := s.requireRestaurant(ctx, restaurantID); err != nil {
		return domain.MenuItem{}, err
	}
	if changes.CategoryID != nil {
		if err := s.requireCategory(ctx, restaurantID, *changes.CategoryID); err != nil {
			return domain.MenuItem{}, err
		}
	}
	item, err := s.repo.UpdateItem(ctx, restaurantID, itemID, changes)
	if err != nil {
		return domain.MenuItem{}, fmt.Errorf("update menu item: %w", err)
	}
	return item, nil
}

func (s *Menu) DeleteMenuItem(ctx context.Context, restaurantID, itemID int64) error {
	if err := domain.ValidateMenuID(itemID); err != nil {
		return err
	}
	if err := s.requireRestaurant(ctx, restaurantID); err != nil {
		return err
	}
	if err := s.repo.DeleteItem(ctx, restaurantID, itemID); err != nil {
		return fmt.Errorf("delete menu item: %w", err)
	}
	return nil
}

func (s *Menu) GetRestaurantMenu(ctx context.Context, restaurantID int64) (RestaurantMenu, error) {
	if err := domain.ValidateID(restaurantID); err != nil {
		return RestaurantMenu{}, err
	}
	// GetMenu checks existence and loads the entire menu in one consistent snapshot.
	menu, err := s.repo.GetMenu(ctx, restaurantID)
	if err != nil {
		return RestaurantMenu{}, fmt.Errorf("get restaurant menu: %w", err)
	}
	return menu, nil
}
