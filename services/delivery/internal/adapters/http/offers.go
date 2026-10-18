package httpadapter

import (
	"io"
	"net/http"
	"time"

	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type offerDTO struct {
	ID          int64      `json:"id"`
	DeliveryID  int64      `json:"delivery_id"`
	CourierID   int64      `json:"courier_id"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RespondedAt *time.Time `json:"responded_at"`
}

func toOffer(o domain.CourierOffer) offerDTO {
	return offerDTO{o.ID, o.DeliveryID, o.CourierID, o.Status, o.ExpiresAt, o.CreatedAt, o.RespondedAt}
}
func (h *handler) offers(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	offers, err := h.service.Offers(r.Context(), courier)
	if err != nil {
		h.fail(w, err)
		return
	}
	result := make([]offerDTO, 0, len(offers))
	for _, o := range offers {
		result = append(result, toOffer(o))
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *handler) respondOffer(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	offer, ok := id(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		h.fail(w, domain.ErrInvalid)
		return
	}
	o, err := h.service.RespondOffer(r.Context(), courier, offer, r.Pattern == "POST /courier-offers/{id}/accept")
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOffer(o))
}
