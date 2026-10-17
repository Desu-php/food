package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
)

func TestPostgresMenuLifecycle(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	repo := NewMenu(pool)
	service := application.NewMenu(NewRestaurants(pool), repo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	empty, err := service.GetRestaurantMenu(ctx, 1)
	if err != nil || empty.Categories == nil || len(empty.Categories) != 0 {
		t.Fatalf("empty menu: %+v %v", empty, err)
	}
	if _, err := service.GetRestaurantMenu(ctx, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	pizza, err := service.CreateMenuCategory(ctx, 1, " Pizza ", 2)
	if err != nil {
		t.Fatal(err)
	}
	drinks, err := service.CreateMenuCategory(ctx, 1, "Drinks", 1)
	if err != nil {
		t.Fatal(err)
	}
	emptyCat, err := service.CreateMenuCategory(ctx, 1, "Empty", 1)
	if err != nil {
		t.Fatal(err)
	}
	if pizza.ID <= 0 || pizza.Name != "Pizza" || pizza.CreatedAt.IsZero() || pizza.UpdatedAt.IsZero() {
		t.Fatalf("category: %+v", pizza)
	}
	first, err := service.CreateMenuItem(ctx, 1, pizza.ID, "First", "Description", 6500, "TJS", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateMenuItem(ctx, 1, pizza.ID, "Second", "", 3000, "TJS", false)
	if err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	changedCat, err := service.UpdateMenuCategory(ctx, 1, pizza.ID, domain.MenuCategoryChanges{SortOrder: &zero})
	if err != nil || changedCat.SortOrder != 0 || changedCat.Name != pizza.Name || !changedCat.CreatedAt.Equal(pizza.CreatedAt) {
		t.Fatalf("category patch: %+v %v", changedCat, err)
	}
	price := int64(7000)
	closed := false
	description := ""
	updated, err := service.UpdateMenuItem(ctx, 1, first.ID, domain.MenuItemChanges{PriceMinor: &price, IsAvailable: &closed, Description: &description})
	if err != nil || updated.PriceMinor != price || updated.IsAvailable || updated.Description != "" || updated.Name != first.Name || !updated.CreatedAt.Equal(first.CreatedAt) || !updated.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("item patch: %+v %v", updated, err)
	}
	menu, err := service.GetRestaurantMenu(ctx, 1)
	if err != nil || len(menu.Categories) != 3 {
		t.Fatalf("menu: %+v %v", menu, err)
	}
	if menu.Categories[0].Category.ID != pizza.ID || menu.Categories[1].Category.ID != drinks.ID || menu.Categories[2].Category.ID != emptyCat.ID {
		t.Fatal("category ordering failed")
	}
	items := menu.Categories[0].Items
	if len(items) != 2 || items[0].ID != first.ID || items[1].ID != second.ID || items[0].IsAvailable || items[1].IsAvailable {
		t.Fatalf("item ordering/availability: %+v", items)
	}
	if menu.Categories[1].Items == nil || len(menu.Categories[1].Items) != 0 {
		t.Fatal("empty category lost")
	}
	moved, err := service.UpdateMenuItem(ctx, 1, first.ID, domain.MenuItemChanges{CategoryID: &drinks.ID})
	if err != nil || moved.CategoryID != drinks.ID || moved.PriceMinor != price || moved.IsAvailable {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if err := service.DeleteMenuItem(ctx, 1, first.ID); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	var storedName string
	if err := pool.QueryRow(ctx, "SELECT deleted_at IS NOT NULL,name FROM menu_items WHERE id=$1", first.ID).Scan(&deleted, &storedName); err != nil || !deleted || storedName != first.Name {
		t.Fatalf("not a soft delete: %v %v", deleted, err)
	}
	menu, err = service.GetRestaurantMenu(ctx, 1)
	if err != nil || len(menu.Categories[1].Items) != 0 || len(menu.Categories[0].Items) != 1 {
		t.Fatalf("deleted item still public: %+v %v", menu, err)
	}
	if _, err := service.UpdateMenuItem(ctx, 1, first.ID, domain.MenuItemChanges{PriceMinor: &price}); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
	if err := service.DeleteMenuItem(ctx, 1, first.ID); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
	if _, err := service.UpdateMenuCategory(ctx, 2, pizza.ID, domain.MenuCategoryChanges{SortOrder: &zero}); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatal(err)
	}
	if _, err := service.UpdateMenuItem(ctx, 2, second.ID, domain.MenuItemChanges{PriceMinor: &price}); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
	if err := service.DeleteMenuItem(ctx, 2, second.ID); !errors.Is(err, domain.ErrItemNotFound) {
		t.Fatal(err)
	}
}

func TestPostgresMenuInvariants(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	repo := NewMenu(pool)
	service := application.NewMenu(NewRestaurants(pool), repo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	own, err := service.CreateMenuCategory(ctx, 1, "Own", 0)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := service.CreateMenuCategory(ctx, 2, "Foreign", 0)
	if err != nil {
		t.Fatal(err)
	}
	item, err := service.CreateMenuItem(ctx, 1, own.ID, "Item", "", 100, "TJS", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateMenuItem(ctx, 1, foreign.ID, "Bad", "", 100, "TJS", true); !errors.Is(err, domain.ErrCategoryRestaurantMismatch) {
		t.Fatalf("foreign category create: %v", err)
	}
	if _, err := service.UpdateMenuItem(ctx, 1, item.ID, domain.MenuItemChanges{CategoryID: &foreign.ID}); !errors.Is(err, domain.ErrCategoryRestaurantMismatch) {
		t.Fatalf("foreign category update: %v", err)
	}
	if _, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: foreign.ID, Name: "Bad", PriceMinor: 100, Currency: "TJS"}); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatalf("database FK did not prevent cross-restaurant create: %v", err)
	}
	if _, err := repo.UpdateItem(ctx, 1, item.ID, domain.MenuItemChanges{CategoryID: &foreign.ID}); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatalf("database FK did not prevent cross-restaurant update: %v", err)
	}
	for _, price := range []int64{0, -1} {
		if _, err := service.CreateMenuItem(ctx, 1, own.ID, "Bad", "", price, "TJS", true); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Fatal(err)
		}
		if _, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: own.ID, Name: "Bad", PriceMinor: price, Currency: "TJS"}); !errors.Is(err, domain.ErrInvalidPrice) {
			t.Fatalf("database price CHECK: %v", err)
		}
	}
	badCurrency := "USD"
	if _, err := repo.UpdateItem(ctx, 1, item.ID, domain.MenuItemChanges{Currency: &badCurrency}); !errors.Is(err, domain.ErrInvalidCurrency) {
		t.Fatalf("database currency CHECK: %v", err)
	}
	blank := " "
	if _, err := repo.UpdateItem(ctx, 1, item.ID, domain.MenuItemChanges{Name: &blank}); !errors.Is(err, domain.ErrInvalidName) {
		t.Fatalf("database name CHECK: %v", err)
	}
	negative := int32(-1)
	if _, err := repo.UpdateCategory(ctx, 1, own.ID, domain.MenuCategoryChanges{SortOrder: &negative}); !errors.Is(err, domain.ErrInvalidSortOrder) {
		t.Fatalf("database order CHECK: %v", err)
	}
	if _, err := service.CreateMenuCategory(ctx, 999, "Cat", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.CreateMenuItem(ctx, 999, own.ID, "Item", "", 100, "TJS", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.CreateMenuItem(ctx, 1, 999, "Item", "", 100, "TJS", true); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatal(err)
	}
	if _, err := repo.GetCategory(ctx, 999); !errors.Is(err, domain.ErrCategoryNotFound) {
		t.Fatal(err)
	}
	injection := `Robert'); DROP TABLE menu_items; --`
	injected, err := service.CreateMenuItem(ctx, 1, own.ID, injection, "", 100, "TJS", true)
	if err != nil || injected.Name != injection {
		t.Fatalf("parameterized name: %+v %v", injected, err)
	}
	menu, err := service.GetRestaurantMenu(ctx, 1)
	if err != nil || len(menu.Categories[0].Items) != 2 || menu.Categories[0].Items[0].CategoryID != own.ID {
		t.Fatalf("failed write changed data: %+v %v", menu, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := repo.GetMenu(canceled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("context lost: %v", err)
	}
}

func TestPostgresConcurrentMenuWrites(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	repo := NewMenu(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	category, err := repo.CreateCategory(ctx, domain.MenuCategory{RestaurantID: 1, Name: "Cat"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: category.ID, Name: "Item", PriceMinor: 100, Currency: "TJS", IsAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	price := int64(7000)
	closed := false
	var wg sync.WaitGroup
	for _, changes := range []domain.MenuItemChanges{{PriceMinor: &price}, {IsAvailable: &closed}} {
		wg.Add(1)
		go func(c domain.MenuItemChanges) {
			defer wg.Done()
			if _, err := repo.UpdateItem(ctx, 1, item.ID, c); err != nil {
				t.Error(err)
			}
		}(changes)
	}
	wg.Wait()
	menu, err := repo.GetMenu(ctx, 1)
	if err != nil || menu.Categories[0].Items[0].PriceMinor != price || menu.Categories[0].Items[0].IsAvailable {
		t.Fatalf("concurrent update lost: %+v %v", menu, err)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := repo.UpdateItem(ctx, 1, item.ID, domain.MenuItemChanges{PriceMinor: &price}); err != nil && !errors.Is(err, domain.ErrItemNotFound) {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := repo.DeleteItem(ctx, 1, item.ID); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	menu, err = repo.GetMenu(ctx, 1)
	if err != nil || len(menu.Categories[0].Items) != 0 {
		t.Fatalf("concurrent update resurrected item: %+v %v", menu, err)
	}
}

type menuQueryCounter struct{ count atomic.Int32 }

func (c *menuQueryCounter) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.count.Add(1)
	return ctx
}
func (c *menuQueryCounter) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
}

func TestPostgresMenuQueryBudget(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := NewMenu(pool)
	for range 5 {
		category, err := repo.CreateCategory(ctx, domain.MenuCategory{RestaurantID: 1, Name: "Cat"})
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := repo.CreateItem(ctx, domain.MenuItem{RestaurantID: 1, CategoryID: category.ID, Name: "Item", PriceMinor: 100, Currency: "TJS"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	counter := &menuQueryCounter{}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = counter
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	service := application.NewMenu(NewRestaurants(traced), NewMenu(traced))
	menu, err := service.GetRestaurantMenu(ctx, 1)
	if err != nil || len(menu.Categories) != 5 {
		t.Fatalf("menu: %+v %v", menu, err)
	}
	if queries := counter.count.Load(); queries != 1 {
		t.Fatalf("menu executed %d SQL queries, want 1 (no N+1)", queries)
	}
}

func TestMenuMigrationRollback(t *testing.T) {
	pool := integrationPool(t, "000001_restaurants", "000002_menu")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sql, err := os.ReadFile("../../../migrations/000002_menu.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var categories, items, restaurants bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('menu_categories') IS NOT NULL,to_regclass('menu_items') IS NOT NULL,to_regclass('restaurants') IS NOT NULL").Scan(&categories, &items, &restaurants); err != nil || categories || items || !restaurants {
		t.Fatalf("rollback affected restaurants: %v %v %v %v", categories, items, restaurants, err)
	}
	sql, err = os.ReadFile("../../../migrations/000002_menu.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMenu(pool).GetMenu(ctx, 1); err != nil {
		t.Fatal(err)
	}
}
