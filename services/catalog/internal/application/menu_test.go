package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Desu-php/food/services/catalog/internal/domain"
)

type menuRepositoryStub struct {
	category                 domain.MenuCategory
	item                     domain.MenuItem
	menu                     RestaurantMenu
	errors                   map[string]error
	ctx                      context.Context
	called                   string
	restaurantID, resourceID int64
	categoryChanges          domain.MenuCategoryChanges
	itemChanges              domain.MenuItemChanges
}

func (r *menuRepositoryStub) record(ctx context.Context, operation string) error {
	r.ctx = ctx
	r.called = operation
	return r.errors[operation]
}
func (r *menuRepositoryStub) GetCategory(ctx context.Context, id int64) (domain.MenuCategory, error) {
	return r.category, r.record(ctx, "get category")
}
func (r *menuRepositoryStub) CreateCategory(ctx context.Context, c domain.MenuCategory) (domain.MenuCategory, error) {
	r.category = c
	return c, r.record(ctx, "create category")
}
func (r *menuRepositoryStub) UpdateCategory(ctx context.Context, rid, cid int64, c domain.MenuCategoryChanges) (domain.MenuCategory, error) {
	r.restaurantID = rid
	r.resourceID = cid
	r.categoryChanges = c
	return r.category, r.record(ctx, "update category")
}
func (r *menuRepositoryStub) CreateItem(ctx context.Context, i domain.MenuItem) (domain.MenuItem, error) {
	r.item = i
	return i, r.record(ctx, "create item")
}
func (r *menuRepositoryStub) UpdateItem(ctx context.Context, rid, iid int64, c domain.MenuItemChanges) (domain.MenuItem, error) {
	r.restaurantID = rid
	r.resourceID = iid
	r.itemChanges = c
	return r.item, r.record(ctx, "update item")
}
func (r *menuRepositoryStub) DeleteItem(ctx context.Context, rid, iid int64) error {
	r.restaurantID = rid
	r.resourceID = iid
	return r.record(ctx, "delete item")
}
func (r *menuRepositoryStub) GetMenu(ctx context.Context, rid int64) (RestaurantMenu, error) {
	r.restaurantID = rid
	return r.menu, r.record(ctx, "get menu")
}

func TestMenuUseCases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restaurants := &repositoryStub{restaurant: domain.Restaurant{ID: 1}}
	repo := &menuRepositoryStub{category: domain.MenuCategory{ID: 2, RestaurantID: 1}, menu: RestaurantMenu{RestaurantID: 1}}
	service := NewMenu(restaurants, repo)
	category, err := service.CreateMenuCategory(ctx, 1, " Pizza ", 0)
	if err != nil || category.Name != "Pizza" || repo.ctx != ctx || restaurants.ctx != ctx {
		t.Fatalf("create category: %+v %v", category, err)
	}
	repo.category = domain.MenuCategory{ID: 2, RestaurantID: 1}
	item, err := service.CreateMenuItem(ctx, 1, 2, " Item ", "", 6500, "TJS", false)
	if err != nil || item.Name != "Item" || item.IsAvailable || item.CategoryID != 2 || repo.called != "create item" {
		t.Fatalf("create item: %+v %v", item, err)
	}
	price := int64(7000)
	closed := false
	empty := ""
	zero := int32(0)
	if _, err := service.UpdateMenuItem(ctx, 1, 3, domain.MenuItemChanges{PriceMinor: &price, IsAvailable: &closed, Description: &empty}); err != nil {
		t.Fatal(err)
	}
	if repo.restaurantID != 1 || repo.resourceID != 3 || *repo.itemChanges.PriceMinor != price || *repo.itemChanges.IsAvailable || *repo.itemChanges.Description != "" || repo.itemChanges.CategoryID != nil {
		t.Fatal("partial item changes lost")
	}
	if _, err := service.UpdateMenuCategory(ctx, 1, 2, domain.MenuCategoryChanges{SortOrder: &zero}); err != nil {
		t.Fatal(err)
	}
	if repo.categoryChanges.SortOrder == nil || *repo.categoryChanges.SortOrder != 0 || repo.categoryChanges.Name != nil {
		t.Fatal("zero sort order lost")
	}
	if err := service.DeleteMenuItem(ctx, 1, 3); err != nil || repo.called != "delete item" || repo.resourceID != 3 {
		t.Fatalf("delete: %v", err)
	}
	menu, err := service.GetRestaurantMenu(ctx, 1)
	if err != nil || menu.RestaurantID != 1 || repo.ctx != ctx {
		t.Fatalf("menu: %+v %v", menu, err)
	}
}

func TestMenuUseCasesRejectInvalidWrites(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Menu) error
		want error
	}{
		{"category name", func(s *Menu) error { _, err := s.CreateMenuCategory(context.Background(), 1, " ", 0); return err }, domain.ErrInvalidName},
		{"category sort", func(s *Menu) error { _, err := s.CreateMenuCategory(context.Background(), 1, "Cat", -1); return err }, domain.ErrInvalidSortOrder},
		{"invalid price", func(s *Menu) error {
			_, err := s.CreateMenuItem(context.Background(), 1, 2, "Item", "", 0, "TJS", true)
			return err
		}, domain.ErrInvalidPrice},
		{"empty item patch", func(s *Menu) error {
			_, err := s.UpdateMenuItem(context.Background(), 1, 3, domain.MenuItemChanges{})
			return err
		}, domain.ErrEmptyMenuUpdate},
		{"empty category patch", func(s *Menu) error {
			_, err := s.UpdateMenuCategory(context.Background(), 1, 2, domain.MenuCategoryChanges{})
			return err
		}, domain.ErrEmptyMenuUpdate},
		{"invalid delete", func(s *Menu) error { return s.DeleteMenuItem(context.Background(), 1, 0) }, domain.ErrInvalidMenuID},
		{"invalid menu", func(s *Menu) error { _, err := s.GetRestaurantMenu(context.Background(), 0); return err }, domain.ErrInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restaurants := &repositoryStub{}
			repo := &menuRepositoryStub{}
			if err := tt.run(NewMenu(restaurants, repo)); !errors.Is(err, tt.want) || repo.called != "" || restaurants.called {
				t.Fatalf("invalid write accessed persistence: %v", err)
			}
		})
	}
}

func TestMenuCategoryOwnership(t *testing.T) {
	repo := &menuRepositoryStub{category: domain.MenuCategory{ID: 2, RestaurantID: 2}}
	s := NewMenu(&repositoryStub{restaurant: domain.Restaurant{ID: 1}}, repo)
	if _, err := s.CreateMenuItem(context.Background(), 1, 2, "Item", "", 100, "TJS", true); !errors.Is(err, domain.ErrCategoryRestaurantMismatch) || repo.called != "get category" {
		t.Fatalf("foreign create: %v", err)
	}
	categoryID := int64(2)
	if _, err := s.UpdateMenuItem(context.Background(), 1, 3, domain.MenuItemChanges{CategoryID: &categoryID}); !errors.Is(err, domain.ErrCategoryRestaurantMismatch) || repo.called != "get category" {
		t.Fatalf("foreign update: %v", err)
	}
	repo.errors = map[string]error{"get category": domain.ErrCategoryNotFound}
	if _, err := s.CreateMenuItem(context.Background(), 1, 99, "Item", "", 100, "TJS", true); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatal(err)
	}
}

func TestMenuMissingRestaurantsAndRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	name := "Name"
	available := false
	calls := []func(*Menu) error{
		func(s *Menu) error { _, err := s.CreateMenuCategory(ctx, 1, name, 0); return err },
		func(s *Menu) error {
			_, err := s.UpdateMenuCategory(ctx, 1, 2, domain.MenuCategoryChanges{Name: &name})
			return err
		},
		func(s *Menu) error { _, err := s.CreateMenuItem(ctx, 1, 2, name, "", 100, "TJS", true); return err },
		func(s *Menu) error {
			_, err := s.UpdateMenuItem(ctx, 1, 3, domain.MenuItemChanges{IsAvailable: &available})
			return err
		},
		func(s *Menu) error { return s.DeleteMenuItem(ctx, 1, 3) },
	}
	for _, call := range calls {
		repo := &menuRepositoryStub{}
		if err := call(NewMenu(&repositoryStub{err: domain.ErrNotFound}, repo)); !errors.Is(err, domain.ErrNotFound) || repo.called != "" {
			t.Fatalf("missing restaurant wrote data: %v", err)
		}
	}
	repo := &menuRepositoryStub{errors: map[string]error{"get menu": domain.ErrNotFound}}
	if _, err := NewMenu(&repositoryStub{}, repo).GetRestaurantMenu(ctx, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	failure := errors.New("database failure")
	repo = &menuRepositoryStub{category: domain.MenuCategory{ID: 2, RestaurantID: 1}, errors: map[string]error{
		"create category": failure, "update category": failure, "create item": failure, "update item": failure, "delete item": failure, "get menu": failure,
	}}
	service := NewMenu(&repositoryStub{}, repo)
	for _, call := range calls {
		if err := call(service); !errors.Is(err, failure) {
			t.Fatalf("repository error lost: %v", err)
		}
	}
	if _, err := service.GetRestaurantMenu(ctx, 1); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
