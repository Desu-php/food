package httpadapter

import (
	"io"
	"net/http"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func (h *handler) location(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	if input.Latitude == nil || input.Longitude == nil {
		h.fail(w, domain.ErrInvalid)
		return
	}
	if err := h.service.UpdateLocation(r.Context(), courier, geo.Point{Latitude: *input.Latitude, Longitude: *input.Longitude}); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) retryDispatch(w http.ResponseWriter, r *http.Request) {
	user, ok := h.identity(w, r, false)
	if !ok {
		return
	}
	delivery, ok := id(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) > 0 {
		h.fail(w, domain.ErrInvalid)
		return
	}
	if err := h.service.RetryDispatch(r.Context(), user, delivery); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
