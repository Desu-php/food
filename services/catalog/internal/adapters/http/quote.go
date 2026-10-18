package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

// NewInternalHandler must be served on a separate, private listener. Public
// NewHandler intentionally never registers these routes.
func NewInternalHandler(quotes *application.Quotes, logger *slog.Logger, timeout time.Duration) http.Handler {
	h := &handler{logger: logger}
	mux := http.NewServeMux()
	quoteHandler := func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		var input struct {
			application.QuoteOptions
			RestaurantID int64                   `json:"restaurant_id"`
			Items        []application.QuoteItem `json:"items"`
		}
		if !h.decodeJSON(w, r, &input) {
			return
		}
		var options []application.QuoteOptions
		if r.URL.Path == "/internal/v2/menu/quote" {
			options = append(options, input.QuoteOptions)
		}
		quote, err := quotes.Quote(r.Context(), input.RestaurantID, input.Items, options...)
		if err != nil {
			for _, failure := range []struct {
				err    error
				status int
				code   string
			}{
				{application.ErrInvalidQuote, 400, "invalid_quote"},
				{domain.ErrCoordinates, 400, "invalid_coordinates"},
				{domain.ErrAmountOverflow, 400, "amount_overflow"},
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
			RestaurantLocation *geo.Point              `json:"restaurant_location,omitempty"`
			PickupAddress      string                  `json:"pickup_address,omitempty"`
			RestaurantID       int64                   `json:"restaurant_id"`
			Currency           string                  `json:"currency"`
			Items              []itemDTO               `json:"items"`
			ItemsSubtotalMinor int64                   `json:"items_subtotal_minor"`
			DeliveryFeeMinor   int64                   `json:"delivery_fee_minor"`
			TotalMinor         int64                   `json:"total_minor"`
			DeliveryAvailable  bool                    `json:"delivery_available"`
			UnavailableReason  string                  `json:"unavailable_reason,omitempty"`
			DistanceMeters     *int64                  `json:"distance_meters,omitempty"`
			PricingSnapshot    *domain.PricingSnapshot `json:"pricing_snapshot,omitempty"`
		}{RestaurantLocation: quote.RestaurantLocation, PickupAddress: quote.PickupAddress, RestaurantID: input.RestaurantID, Currency: domain.CurrencyTJS, Items: result, ItemsSubtotalMinor: quote.ItemsSubtotalMinor, DeliveryFeeMinor: quote.DeliveryFeeMinor, TotalMinor: quote.TotalMinor, DeliveryAvailable: quote.DeliveryAvailable, UnavailableReason: quote.UnavailableReason, DistanceMeters: quote.DistanceMeters, PricingSnapshot: quote.PricingSnapshot})
	}
	mux.HandleFunc("POST /internal/v1/menu/quote", quoteHandler)
	mux.HandleFunc("POST /internal/v2/menu/quote", quoteHandler)
	mux.HandleFunc("/internal/v2/menu/quote", h.methodNotAllowed("POST"))
	mux.HandleFunc("/internal/v1/menu/quote", h.methodNotAllowed("POST"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { h.writeError(w, 404, "endpoint not found") })
	return h.withMiddleware(mux, timeout)
}
