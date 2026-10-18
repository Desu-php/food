package httpadapter

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

// Mutable menu data exists only in this HTTP test fake, never in production.
type testMenuRepository struct {
	mu                     sync.Mutex
	restaurants            *testRepository
	categories             map[int64]domain.MenuCategory
	items                  map[int64]domain.MenuItem
	deleted                map[int64]bool
	nextCategory, nextItem int64
}

func (r *testMenuRepository) GetCategory(ctx context.Context, id int64) (domain.MenuCategory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	category, ok := r.categories[id]
	if !ok {
		return domain.MenuCategory{}, domain.ErrCategoryNotFound
	}
	return category, nil
}
func (r *testMenuRepository) CreateCategory(ctx context.Context, c domain.MenuCategory) (domain.MenuCategory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.ID = r.nextCategory
	r.nextCategory++
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	r.categories[c.ID] = c
	return c, nil
}
func (r *testMenuRepository) UpdateCategory(ctx context.Context, rid, cid int64, changes domain.MenuCategoryChanges) (domain.MenuCategory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.categories[cid]
	if !ok || c.RestaurantID != rid {
		return domain.MenuCategory{}, domain.ErrCategoryNotFound
	}
	if changes.Name != nil {
		c.Name = *changes.Name
	}
	if changes.SortOrder != nil {
		c.SortOrder = *changes.SortOrder
	}
	c.UpdatedAt = time.Now().UTC()
	r.categories[cid] = c
	return c, nil
}
func (r *testMenuRepository) CreateItem(ctx context.Context, i domain.MenuItem) (domain.MenuItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i.ID = r.nextItem
	r.nextItem++
	i.CreatedAt = time.Now().UTC()
	i.UpdatedAt = i.CreatedAt
	r.items[i.ID] = i
	return i, nil
}
func (r *testMenuRepository) UpdateItem(ctx context.Context, rid, iid int64, c domain.MenuItemChanges) (domain.MenuItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.items[iid]
	if !ok || i.RestaurantID != rid || r.deleted[iid] {
		return domain.MenuItem{}, domain.ErrItemNotFound
	}
	if c.CategoryID != nil {
		i.CategoryID = *c.CategoryID
	}
	if c.Name != nil {
		i.Name = *c.Name
	}
	if c.Description != nil {
		i.Description = *c.Description
	}
	if c.PriceMinor != nil {
		i.PriceMinor = *c.PriceMinor
	}
	if c.Currency != nil {
		i.Currency = *c.Currency
	}
	if c.IsAvailable != nil {
		i.IsAvailable = *c.IsAvailable
	}
	i.UpdatedAt = time.Now().UTC()
	r.items[iid] = i
	return i, nil
}
func (r *testMenuRepository) DeleteItem(ctx context.Context, rid, iid int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.items[iid]
	if !ok || i.RestaurantID != rid || r.deleted[iid] {
		return domain.ErrItemNotFound
	}
	r.deleted[iid] = true
	return nil
}
func (r *testMenuRepository) GetMenu(ctx context.Context, rid int64) (application.RestaurantMenu, error) {
	if _, err := r.restaurants.Get(ctx, rid); err != nil {
		return application.RestaurantMenu{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	menu := application.RestaurantMenu{RestaurantID: rid, Categories: make([]application.MenuCategoryGroup, 0)}
	for _, c := range r.categories {
		if c.RestaurantID == rid {
			group := application.MenuCategoryGroup{Category: c, Items: make([]domain.MenuItem, 0)}
			for _, i := range r.items {
				if i.RestaurantID == rid && i.CategoryID == c.ID && !r.deleted[i.ID] {
					group.Items = append(group.Items, i)
				}
			}
			slices.SortFunc(group.Items, func(a, b domain.MenuItem) int { return cmp.Compare(a.ID, b.ID) })
			menu.Categories = append(menu.Categories, group)
		}
	}
	slices.SortFunc(menu.Categories, func(a, b application.MenuCategoryGroup) int {
		if order := cmp.Compare(a.Category.SortOrder, b.Category.SortOrder); order != 0 {
			return order
		}
		return cmp.Compare(a.Category.ID, b.Category.ID)
	})
	return menu, nil
}

func newMenuTestHandler() http.Handler {
	restaurants := &testRepository{rows: []domain.Restaurant{{ID: 1, Name: "Pizza House", IsOpen: true}, {ID: 2, Name: "Burger Corner"}}, nextID: 3}
	repo := &testMenuRepository{restaurants: restaurants, categories: map[int64]domain.MenuCategory{}, items: map[int64]domain.MenuItem{}, deleted: map[int64]bool{}, nextCategory: 1, nextItem: 1}
	return authenticatedCatalog(NewHandler(application.NewRestaurants(restaurants), application.NewMenu(restaurants, repo), func(context.Context) error { return nil }, slog.New(slog.NewJSONHandler(io.Discard, nil)), time.Second))
}

func TestMenuHTTPLifecycle(t *testing.T) {
	h := newMenuTestHandler()
	response := request(h, "GET", "/restaurants/1/menu", "")
	checkJSON(t, response, 200)
	if strings.TrimSpace(response.Body.String()) != `{"restaurant_id":1,"categories":[]}` {
		t.Fatal(response.Body.String())
	}
	response = request(h, "POST", "/restaurants/1/categories", `{"name":" Pizza ","sort_order":2}`)
	checkJSON(t, response, 201)
	var category categoryDTO
	if err := json.Unmarshal(response.Body.Bytes(), &category); err != nil {
		t.Fatal(err)
	}
	if category.ID != 1 || category.RestaurantID != 1 || category.Name != "Pizza" || category.SortOrder != 2 || category.CreatedAt.IsZero() {
		t.Fatalf("category: %+v", category)
	}
	if response.Header().Get("Location") != "/restaurants/1/categories/1" {
		t.Fatal("category location missing")
	}
	response = request(h, "POST", "/restaurants/1/categories", `{"name":" Drinks ","sort_order":1}`)
	checkJSON(t, response, 201)
	response = request(h, "POST", "/restaurants/1/categories", `{"name":"Empty","sort_order":1}`)
	checkJSON(t, response, 201)
	response = request(h, "PATCH", "/restaurants/1/categories/1", `{"sort_order":0}`)
	checkJSON(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &category); err != nil {
		t.Fatal(err)
	}
	if category.Name != "Pizza" || category.SortOrder != 0 {
		t.Fatalf("category patch: %+v", category)
	}
	response = request(h, "POST", "/restaurants/1/items", `{"category_id":1,"name":" Margherita ","description":"Cheese","price_minor":6500,"currency":"TJS"}`)
	checkJSON(t, response, 201)
	var created itemDTO
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 || created.RestaurantID != 1 || created.CategoryID != 1 || created.Name != "Margherita" || !created.IsAvailable || created.PriceMinor != 6500 || created.CreatedAt.IsZero() {
		t.Fatalf("item: %+v", created)
	}
	if response.Header().Get("Location") != "/restaurants/1/items/1" {
		t.Fatal("item location missing")
	}
	response = request(h, "POST", "/restaurants/1/items", `{"category_id":1,"name":"Second","price_minor":3000,"currency":"TJS","is_available":false}`)
	checkJSON(t, response, 201)
	response = request(h, "PATCH", "/restaurants/1/items/1", `{"price_minor":7000,"is_available":false,"description":""}`)
	checkJSON(t, response, 200)
	var updated itemDTO
	if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.IsAvailable || updated.PriceMinor != 7000 || updated.Description != "" || updated.Name != created.Name || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("partial item update: %+v", updated)
	}
	response = request(h, "GET", "/restaurants/1/menu", "")
	checkJSON(t, response, 200)
	var menu publicMenuDTO
	if err := json.Unmarshal(response.Body.Bytes(), &menu); err != nil {
		t.Fatal(err)
	}
	if len(menu.Categories) != 3 || menu.Categories[0].ID != 1 || menu.Categories[1].ID != 2 || menu.Categories[2].ID != 3 {
		t.Fatalf("category order: %+v", menu)
	}
	if len(menu.Categories[0].Items) != 2 || menu.Categories[0].Items[0].ID != 1 || menu.Categories[0].Items[1].ID != 2 || menu.Categories[0].Items[0].IsAvailable {
		t.Fatalf("item order/availability: %+v", menu)
	}
	if menu.Categories[1].Items == nil || len(menu.Categories[1].Items) != 0 {
		t.Fatal("empty category must contain []")
	}
	if strings.Contains(response.Body.String(), "created_at") || strings.Contains(response.Body.String(), "deleted_at") {
		t.Fatal("public DTO exposes internal fields")
	}
	response = request(h, "PATCH", "/restaurants/1/items/1", `{"category_id":2}`)
	checkJSON(t, response, 200)
	response = request(h, "GET", "/restaurants/1/menu", "")
	checkJSON(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &menu); err != nil {
		t.Fatal(err)
	}
	if len(menu.Categories[0].Items) != 1 || menu.Categories[1].Items[0].ID != 1 {
		t.Fatal("item did not move category")
	}
	response = request(h, "DELETE", "/restaurants/1/items/1", "")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	response = request(h, "GET", "/restaurants/1/menu", "")
	checkJSON(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &menu); err != nil {
		t.Fatal(err)
	}
	if len(menu.Categories[1].Items) != 0 || len(menu.Categories[0].Items) != 1 {
		t.Fatal("deleted item remained in public menu")
	}
	checkJSON(t, request(h, "PATCH", "/restaurants/1/items/1", `{"name":"Restore"}`), 404)
	checkJSON(t, request(h, "DELETE", "/restaurants/1/items/1", ""), 404)
	checkJSON(t, request(h, "GET", "/restaurants/1", ""), 200)
}

func TestMenuHTTPValidation(t *testing.T) {
	validItem := `{"category_id":1,"name":"Item","price_minor":100,"currency":"TJS"}`
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"restaurant missing", "GET", "/restaurants/999/menu", "", 404},
		{"invalid restaurant", "GET", "/restaurants/abc/menu", "", 400},
		{"invalid resource", "PATCH", "/restaurants/1/items/0", `{"name":"Item"}`, 400},
		{"overflow id", "DELETE", "/restaurants/1/items/9223372036854775808", "", 400},
		{"create missing restaurant", "POST", "/restaurants/999/categories", `{"name":"Cat"}`, 404},
		{"empty category", "POST", "/restaurants/1/categories", `{"name":" "}`, 400},
		{"negative order", "PATCH", "/restaurants/1/categories/1", `{"sort_order":-1}`, 400},
		{"null order", "PATCH", "/restaurants/1/categories/1", `{"sort_order":null}`, 400},
		{"category id read only", "POST", "/restaurants/1/categories", `{"name":"Cat","id":5}`, 400},
		{"empty category patch", "PATCH", "/restaurants/1/categories/1", `{}`, 400},
		{"wrong category scope", "PATCH", "/restaurants/2/categories/1", `{"name":"Cat"}`, 404},
		{"foreign category create", "POST", "/restaurants/2/items", validItem, 400},
		{"missing category", "POST", "/restaurants/1/items", `{"category_id":999,"name":"Item","price_minor":100,"currency":"TJS"}`, 404},
		{"no category", "POST", "/restaurants/1/items", `{"name":"Item","price_minor":100,"currency":"TJS"}`, 400},
		{"zero price", "POST", "/restaurants/1/items", `{"category_id":1,"name":"Item","price_minor":0,"currency":"TJS"}`, 400},
		{"negative price", "PATCH", "/restaurants/1/items/1", `{"price_minor":-10}`, 400},
		{"float price", "PATCH", "/restaurants/1/items/1", `{"price_minor":1.5}`, 400},
		{"overflow price", "PATCH", "/restaurants/1/items/1", `{"price_minor":9223372036854775808}`, 400},
		{"zero price patch", "PATCH", "/restaurants/1/items/1", `{"price_minor":0}`, 400},
		{"bad currency", "PATCH", "/restaurants/1/items/1", `{"currency":"USD"}`, 400},
		{"no currency", "POST", "/restaurants/1/items", `{"category_id":1,"name":"Item","price_minor":100}`, 400},
		{"empty item name", "PATCH", "/restaurants/1/items/1", `{"name":""}`, 400},
		{"null availability", "PATCH", "/restaurants/1/items/1", `{"is_available":null}`, 400},
		{"null description", "PATCH", "/restaurants/1/items/1", `{"description":null}`, 400},
		{"empty item patch", "PATCH", "/restaurants/1/items/1", `{}`, 400},
		{"server fields", "PATCH", "/restaurants/1/items/1", `{"restaurant_id":2}`, 400},
		{"soft delete controlled", "PATCH", "/restaurants/1/items/1", `{"deleted_at":null}`, 400},
		{"foreign category patch", "PATCH", "/restaurants/1/items/1", `{"category_id":2}`, 400},
		{"wrong item scope", "PATCH", "/restaurants/2/items/1", `{"is_available":false}`, 404},
		{"wrong delete scope", "DELETE", "/restaurants/2/items/1", "", 404},
		{"delete missing restaurant", "DELETE", "/restaurants/999/items/1", "", 404},
		{"missing item", "PATCH", "/restaurants/1/items/999", `{"name":"Item"}`, 404},
		{"malformed JSON", "POST", "/restaurants/1/items", `{"name":`, 400},
		{"multiple JSON", "POST", "/restaurants/1/items", validItem + `{}`, 400},
		{"oversized body", "POST", "/restaurants/1/items", `{"name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413},
		{"unsupported menu method", "POST", "/restaurants/1/menu", `{}`, 405},
		{"no item get", "GET", "/restaurants/1/items/1", "", 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMenuTestHandler()
			checkJSON(t, request(h, "POST", "/restaurants/1/categories", `{"name":"Cat"}`), 201)
			checkJSON(t, request(h, "POST", "/restaurants/2/categories", `{"name":"Foreign"}`), 201)
			checkJSON(t, request(h, "POST", "/restaurants/1/items", validItem), 201)
			response := request(h, tt.method, tt.path, tt.body)
			checkJSON(t, response, tt.status)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] == "" {
				t.Fatal("missing JSON error")
			}
			unchanged := request(h, "GET", "/restaurants/1/menu", "")
			var menu publicMenuDTO
			if err := json.Unmarshal(unchanged.Body.Bytes(), &menu); err != nil {
				t.Fatal(err)
			}
			if len(menu.Categories) != 1 || len(menu.Categories[0].Items) != 1 || menu.Categories[0].Items[0].PriceMinor != 100 || !menu.Categories[0].Items[0].IsAvailable {
				t.Fatal("rejected mutation changed menu")
			}
		})
	}
	req := httptest.NewRequest("POST", "/restaurants/1/categories", strings.NewReader(`{"name":"Cat"}`))
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	newMenuTestHandler().ServeHTTP(response, req)
	checkJSON(t, response, 415)
}
