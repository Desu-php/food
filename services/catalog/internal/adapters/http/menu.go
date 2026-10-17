package httpadapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
)

func (h *handler) registerPublicMenuRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /restaurants/{restaurantId}/menu", h.getMenu)
	mux.HandleFunc("/restaurants/{restaurantId}/menu", h.methodNotAllowed("GET, HEAD"))
}

// Each mutation checks the authenticated principal and restaurant grant.
func (h *handler) registerAdministrativeMenuRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /restaurants/{restaurantId}/categories", h.createCategory)
	mux.HandleFunc("PATCH /restaurants/{restaurantId}/categories/{categoryId}", h.updateCategory)
	mux.HandleFunc("POST /restaurants/{restaurantId}/items", h.createItem)
	mux.HandleFunc("PATCH /restaurants/{restaurantId}/items/{itemId}", h.updateItem)
	mux.HandleFunc("DELETE /restaurants/{restaurantId}/items/{itemId}", h.deleteItem)
	mux.HandleFunc("/restaurants/{restaurantId}/categories", h.methodNotAllowed("POST"))
	mux.HandleFunc("/restaurants/{restaurantId}/categories/{categoryId}", h.methodNotAllowed("PATCH"))
	mux.HandleFunc("/restaurants/{restaurantId}/items", h.methodNotAllowed("POST"))
	mux.HandleFunc("/restaurants/{restaurantId}/items/{itemId}", h.methodNotAllowed("PATCH, DELETE"))
}

type categoryDTO struct {
	ID           int64     `json:"id"`
	RestaurantID int64     `json:"restaurant_id"`
	Name         string    `json:"name"`
	SortOrder    int32     `json:"sort_order"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func categoryResponse(c domain.MenuCategory) categoryDTO {
	return categoryDTO{c.ID, c.RestaurantID, c.Name, c.SortOrder, c.CreatedAt.UTC(), c.UpdatedAt.UTC()}
}

type itemDTO struct {
	ID           int64     `json:"id"`
	RestaurantID int64     `json:"restaurant_id"`
	CategoryID   int64     `json:"category_id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	PriceMinor   int64     `json:"price_minor"`
	Currency     string    `json:"currency"`
	IsAvailable  bool      `json:"is_available"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func itemResponse(i domain.MenuItem) itemDTO {
	return itemDTO{i.ID, i.RestaurantID, i.CategoryID, i.Name, i.Description, i.PriceMinor, i.Currency, i.IsAvailable, i.CreatedAt.UTC(), i.UpdatedAt.UTC()}
}

type publicMenuDTO struct {
	RestaurantID int64               `json:"restaurant_id"`
	Categories   []publicCategoryDTO `json:"categories"`
}

type publicCategoryDTO struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	SortOrder int32           `json:"sort_order"`
	Items     []publicItemDTO `json:"items"`
}

type publicItemDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	IsAvailable bool   `json:"is_available"`
}

func menuResponse(menu application.RestaurantMenu) publicMenuDTO {
	result := publicMenuDTO{RestaurantID: menu.RestaurantID, Categories: make([]publicCategoryDTO, 0, len(menu.Categories))}
	for _, group := range menu.Categories {
		category := publicCategoryDTO{ID: group.Category.ID, Name: group.Category.Name, SortOrder: group.Category.SortOrder, Items: make([]publicItemDTO, 0, len(group.Items))}
		for _, i := range group.Items {
			category.Items = append(category.Items, publicItemDTO{i.ID, i.Name, i.Description, i.PriceMinor, i.Currency, i.IsAvailable})
		}
		result.Categories = append(result.Categories, category)
	}
	return result
}

// A pointer preserves the distinction between omitted and explicitly zero fields.
// RawMessage also lets us reject null instead of silently treating it as omission.
func optionalField[T any](raw json.RawMessage, field string) (*T, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value *T
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("%s has an invalid value", field)
	}
	return value, nil
}

type categoryMutationDTO struct {
	Name      json.RawMessage `json:"name"`
	SortOrder json.RawMessage `json:"sort_order"`
}

func (d categoryMutationDTO) changes() (domain.MenuCategoryChanges, error) {
	name, err := optionalField[string](d.Name, "name")
	if err != nil {
		return domain.MenuCategoryChanges{}, err
	}
	order, err := optionalField[int32](d.SortOrder, "sort_order")
	if err != nil {
		return domain.MenuCategoryChanges{}, err
	}
	return domain.MenuCategoryChanges{Name: name, SortOrder: order}, nil
}

type itemMutationDTO struct {
	CategoryID  json.RawMessage `json:"category_id"`
	Name        json.RawMessage `json:"name"`
	Description json.RawMessage `json:"description"`
	PriceMinor  json.RawMessage `json:"price_minor"`
	Currency    json.RawMessage `json:"currency"`
	IsAvailable json.RawMessage `json:"is_available"`
}

func (d itemMutationDTO) changes() (domain.MenuItemChanges, error) {
	var c domain.MenuItemChanges
	var err error
	c.CategoryID, err = optionalField[int64](d.CategoryID, "category_id")
	if err != nil {
		return c, err
	}
	c.Name, err = optionalField[string](d.Name, "name")
	if err != nil {
		return c, err
	}
	c.Description, err = optionalField[string](d.Description, "description")
	if err != nil {
		return c, err
	}
	c.PriceMinor, err = optionalField[int64](d.PriceMinor, "price_minor")
	if err != nil {
		return c, err
	}
	c.Currency, err = optionalField[string](d.Currency, "currency")
	if err != nil {
		return c, err
	}
	c.IsAvailable, err = optionalField[bool](d.IsAvailable, "is_available")
	return c, err
}

func menuPathIDs(r *http.Request, child string) (int64, int64, error) {
	restaurantID, err := strconv.ParseInt(r.PathValue("restaurantId"), 10, 64)
	if err != nil || restaurantID <= 0 {
		return 0, 0, domain.ErrInvalidID
	}
	if child == "" {
		return restaurantID, 0, nil
	}
	id, err := strconv.ParseInt(r.PathValue(child), 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, domain.ErrInvalidMenuID
	}
	return restaurantID, id, nil
}

func (h *handler) getMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, _, err := menuPathIDs(r, "")
	if err != nil {
		h.handleError(w, err)
		return
	}
	menu, err := h.menu.GetRestaurantMenu(r.Context(), restaurantID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, menuResponse(menu))
}

func (h *handler) decodeCategory(w http.ResponseWriter, r *http.Request) (domain.MenuCategoryChanges, bool) {
	var dto categoryMutationDTO
	if !h.decodeJSON(w, r, &dto) {
		return domain.MenuCategoryChanges{}, false
	}
	changes, err := dto.changes()
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return domain.MenuCategoryChanges{}, false
	}
	return changes, true
}

func (h *handler) createCategory(w http.ResponseWriter, r *http.Request) {
	grantID, _, _ := menuPathIDs(r, "")
	if !h.authorize(w, r, grantID) {
		return
	}
	restaurantID, _, err := menuPathIDs(r, "")
	if err != nil {
		h.handleError(w, err)
		return
	}
	input, ok := h.decodeCategory(w, r)
	if !ok {
		return
	}
	var name string
	var order int32
	if input.Name != nil {
		name = *input.Name
	}
	if input.SortOrder != nil {
		order = *input.SortOrder
	}
	category, err := h.menu.CreateMenuCategory(r.Context(), restaurantID, name, order)
	if err != nil {
		h.handleError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/restaurants/%d/categories/%d", restaurantID, category.ID))
	h.writeJSON(w, http.StatusCreated, categoryResponse(category))
}

func (h *handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	grantID, _, _ := menuPathIDs(r, "")
	if !h.authorize(w, r, grantID) {
		return
	}
	restaurantID, categoryID, err := menuPathIDs(r, "categoryId")
	if err != nil {
		h.handleError(w, err)
		return
	}
	changes, ok := h.decodeCategory(w, r)
	if !ok {
		return
	}
	category, err := h.menu.UpdateMenuCategory(r.Context(), restaurantID, categoryID, changes)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, categoryResponse(category))
}

func (h *handler) decodeItem(w http.ResponseWriter, r *http.Request) (domain.MenuItemChanges, bool) {
	var dto itemMutationDTO
	if !h.decodeJSON(w, r, &dto) {
		return domain.MenuItemChanges{}, false
	}
	changes, err := dto.changes()
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return domain.MenuItemChanges{}, false
	}
	return changes, true
}

func (h *handler) createItem(w http.ResponseWriter, r *http.Request) {
	grantID, _, _ := menuPathIDs(r, "")
	if !h.authorize(w, r, grantID) {
		return
	}
	restaurantID, _, err := menuPathIDs(r, "")
	if err != nil {
		h.handleError(w, err)
		return
	}
	input, ok := h.decodeItem(w, r)
	if !ok {
		return
	}
	var categoryID, price int64
	var name, description, currency string
	available := true
	if input.CategoryID != nil {
		categoryID = *input.CategoryID
	}
	if input.Name != nil {
		name = *input.Name
	}
	if input.Description != nil {
		description = *input.Description
	}
	if input.PriceMinor != nil {
		price = *input.PriceMinor
	}
	if input.Currency != nil {
		currency = *input.Currency
	}
	if input.IsAvailable != nil {
		available = *input.IsAvailable
	}
	item, err := h.menu.CreateMenuItem(r.Context(), restaurantID, categoryID, name, description, price, currency, available)
	if err != nil {
		h.handleError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/restaurants/%d/items/%d", restaurantID, item.ID))
	h.writeJSON(w, http.StatusCreated, itemResponse(item))
}

func (h *handler) updateItem(w http.ResponseWriter, r *http.Request) {
	grantID, _, _ := menuPathIDs(r, "")
	if !h.authorize(w, r, grantID) {
		return
	}
	restaurantID, itemID, err := menuPathIDs(r, "itemId")
	if err != nil {
		h.handleError(w, err)
		return
	}
	changes, ok := h.decodeItem(w, r)
	if !ok {
		return
	}
	item, err := h.menu.UpdateMenuItem(r.Context(), restaurantID, itemID, changes)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, itemResponse(item))
}

func (h *handler) deleteItem(w http.ResponseWriter, r *http.Request) {
	grantID, _, _ := menuPathIDs(r, "")
	if !h.authorize(w, r, grantID) {
		return
	}
	restaurantID, itemID, err := menuPathIDs(r, "itemId")
	if err != nil {
		h.handleError(w, err)
		return
	}
	if err := h.menu.DeleteMenuItem(r.Context(), restaurantID, itemID); err != nil {
		h.handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
