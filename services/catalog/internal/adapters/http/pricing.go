package httpadapter

import (
	"net/http"

	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func (h *handler) registerPricingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /restaurants/{restaurantId}/delivery-settings", h.pricingSettings)
	mux.HandleFunc("PUT /restaurants/{restaurantId}/delivery-settings", h.pricingSettings)
	mux.HandleFunc("POST /restaurants/{restaurantId}/delivery-tariffs", h.tariff)
	mux.HandleFunc("PUT /restaurants/{restaurantId}/delivery-tariffs/{tariffId}", h.tariff)
	mux.HandleFunc("DELETE /restaurants/{restaurantId}/delivery-tariffs/{tariffId}", h.tariff)
	mux.HandleFunc("/restaurants/{restaurantId}/delivery-settings", h.methodNotAllowed("GET, HEAD, PUT"))
	mux.HandleFunc("/restaurants/{restaurantId}/delivery-tariffs", h.methodNotAllowed("POST"))
	mux.HandleFunc("/restaurants/{restaurantId}/delivery-tariffs/{tariffId}", h.methodNotAllowed("PUT, DELETE"))
}
func (h *handler) pricingSettings(w http.ResponseWriter, r *http.Request) {
	id, _, err := menuPathIDs(r, "")
	if err != nil {
		h.handleError(w, err)
		return
	}
	if !h.authorize(w, r, id) {
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		p, err := h.pricing.Get(r.Context(), id)
		if err != nil {
			h.handleError(w, err)
			return
		}
		h.writeJSON(w, 200, p)
		return
	}
	var body struct {
		DeliveryEnabled *bool  `json:"delivery_enabled"`
		PickupEnabled   *bool  `json:"pickup_enabled"`
		Minimum         *int64 `json:"minimum_delivery_subtotal_minor"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	if body.DeliveryEnabled == nil || body.PickupEnabled == nil || body.Minimum == nil {
		h.writeError(w, 400, "all delivery settings are required")
		return
	}
	p, err := h.pricing.Update(r.Context(), id, domain.DeliverySettings{DeliveryEnabled: *body.DeliveryEnabled, PickupEnabled: *body.PickupEnabled, MinimumDeliverySubtotalMinor: *body.Minimum})
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, 200, p)
}
func (h *handler) tariff(w http.ResponseWriter, r *http.Request) {
	child := ""
	if r.Method != http.MethodPost {
		child = "tariffId"
	}
	restaurant, id, err := menuPathIDs(r, child)
	if err != nil {
		h.handleError(w, err)
		return
	}
	if !h.authorize(w, r, restaurant) {
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.pricing.DeleteTariff(r.Context(), restaurant, id); err != nil {
			h.handleError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	var body struct {
		MaxDistanceMeters *int64 `json:"max_distance_meters"`
		DeliveryFeeMinor  *int64 `json:"delivery_fee_minor"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	if body.MaxDistanceMeters == nil || body.DeliveryFeeMinor == nil {
		h.writeError(w, 400, "maximum distance and delivery fee are required")
		return
	}
	t, err := h.pricing.SaveTariff(r.Context(), domain.DeliveryTariff{ID: id, RestaurantID: restaurant, MaxDistanceMeters: *body.MaxDistanceMeters, DeliveryFeeMinor: *body.DeliveryFeeMinor})
	if err != nil {
		h.handleError(w, err)
		return
	}
	status := 200
	if r.Method == http.MethodPost {
		status = 201
	}
	h.writeJSON(w, status, t)
}
