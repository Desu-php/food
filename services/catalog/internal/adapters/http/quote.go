package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"example.com/food/catalog/internal/application"
	"example.com/food/catalog/internal/domain"
)

// NewInternalHandler must be served on a separate, private listener. Public
// NewHandler intentionally never registers these routes.
func NewInternalHandler(quotes *application.Quotes, logger *slog.Logger, timeout time.Duration) http.Handler {
	h := &handler{logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/menu/quote", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		var input struct {
			RestaurantID int64                   `json:"restaurant_id"`
			Items        []application.QuoteItem `json:"items"`
		}
		if !h.decodeJSON(w, r, &input) {
			return
		}
		quote, err := quotes.Quote(r.Context(), input.RestaurantID, input.Items)
		if err != nil {
			for _, failure := range []struct {
				err    error
				status int
				code   string
			}{
				{application.ErrInvalidQuote, 400, "invalid_quote"},
				{domain.ErrNotFound, 404, "restaurant_not_found"},
				{domain.ErrItemNotFound, 404, "item_not_found"},
				{application.ErrRestaurantUnavailable, 409, "restaurant_unavailable"},
				{application.ErrItemUnavailable, 409, "item_unavailable"},
				{application.ErrItemRestaurantMismatch, 409, "item_restaurant_mismatch"},
			} {
				if errors.Is(err, failure.err) {
					h.writeJSON(w, failure.status, map[string]string{"error": failure.err.Error(), "code": failure.code})
					return
				}
			}
			h.handleError(w, err)
			return
		}
		type itemDTO struct {
			MenuItemID     int64  `json:"menu_item_id"`
			RestaurantID   int64  `json:"restaurant_id"`
			Name           string `json:"name"`
			Quantity       int64  `json:"quantity"`
			UnitPriceMinor int64  `json:"unit_price_minor"`
			Currency       string `json:"currency"`
		}
		quantities := make(map[int64]int64, len(input.Items))
		for _, p := range input.Items {
			quantities[p.MenuItemID] = p.Quantity
		}
		items := quote.Items
		result := make([]itemDTO, 0, len(items))
		for _, p := range items {
			result = append(result, itemDTO{p.ID, p.RestaurantID, p.Name, quantities[p.ID], p.PriceMinor, p.Currency})
		}
		h.writeJSON(w, 200, struct {
			PickupAddress string    `json:"pickup_address,omitempty"`
			RestaurantID  int64     `json:"restaurant_id"`
			Currency      string    `json:"currency"`
			Items         []itemDTO `json:"items"`
		}{quote.PickupAddress, input.RestaurantID, domain.CurrencyTJS, result})
	})
	mux.HandleFunc("/internal/v1/menu/quote", h.methodNotAllowed("POST"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { h.writeError(w, 404, "endpoint not found") })
	return h.withMiddleware(mux, timeout)
}
