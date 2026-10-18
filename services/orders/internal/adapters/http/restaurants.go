package httpadapter

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func (h *handler) restaurantAction(w http.ResponseWriter, r *http.Request) {
	user, ok := h.restaurantIdentity(w, r)
	if !ok {
		return
	}
	restaurant, err := strconv.ParseInt(r.PathValue("restaurant"), 10, 64)
	if err != nil || restaurant <= 0 {
		h.handleError(w, application.ErrInvalidRequest)
		return
	}

	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		allowed := (p.Has("restaurant_staff") || p.Has("admin")) && p.Restaurant(restaurant)
		if r.Method == http.MethodGet && r.PathValue("id") != "" {
			allowed = allowed || (p.Has("dispatcher") && p.Can("saga:retry") && p.Restaurant(restaurant))
		}
		if r.PathValue("action") == "retry" {
			allowed = (p.Has("dispatcher") || p.Has("admin")) && p.Can("saga:retry") && p.Restaurant(restaurant)
		}
		if !allowed {
			h.writeError(w, 403, "restaurant access denied")
			return
		}
	}
	if h.restaurants == nil {
		h.writeError(w, 403, "restaurant access denied")
		return
	}
	if r.Method == http.MethodGet && r.PathValue("id") != "" {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			h.handleError(w, application.ErrInvalidRequest)
			return
		}
		s, err := h.restaurants.GetSaga(r.Context(), user, restaurant, id)
		if err != nil {
			h.handleError(w, err)
			return
		}
		h.writeJSON(w, 200, toSagaDTO(s))
		return
	}
	if r.Method == http.MethodGet {
		limit := 20
		after := int64(0)
		if v := r.URL.Query().Get("limit"); v != "" {
			limit, err = strconv.Atoi(v)
			if err != nil {
				h.handleError(w, application.ErrInvalidRequest)
				return
			}
		}
		if v := r.URL.Query().Get("after"); v != "" {
			after, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				h.handleError(w, application.ErrInvalidRequest)
				return
			}
		}
		orders, err := h.restaurants.Incoming(r.Context(), user, restaurant, after, limit)
		if err != nil {
			h.handleError(w, err)
			return
		}
		result := make([]orderDTO, 0, len(orders))
		for _, o := range orders {
			result = append(result, toDTO(o))
		}
		h.writeJSON(w, 200, result)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.handleError(w, application.ErrInvalidRequest)
		return
	}
	if r.PathValue("action") == "verify-pickup" {
		var input struct {
			Code string `json:"pickup_code"`
		}
		if !h.decodeJSON(w, r, &input) {
			return
		}
		o, err := h.restaurants.VerifyPickup(r.Context(), user, restaurant, id, input.Code)
		if err != nil {
			h.handleError(w, err)
			return
		}
		h.writeJSON(w, 200, toDTO(o))
		return
	}
	if r.PathValue("action") == "complete-pickup" {
		var input struct {
			Code             string `json:"pickup_code"`
			PaymentConfirmed bool   `json:"payment_confirmed"`
			ReceiptReference string `json:"receipt_reference"`
		}
		if !h.decodeJSON(w, r, &input) {
			return
		}
		o, err := h.restaurants.CompletePickup(r.Context(), user, restaurant, id, application.PickupCompletion{Code: input.Code, PaymentConfirmed: input.PaymentConfirmed, ReceiptReference: input.ReceiptReference})
		if err != nil {
			h.handleError(w, err)
			return
		}
		h.writeJSON(w, 200, toDTO(o))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) > 0 {
		h.writeError(w, 400, "action body must be empty")
		return
	}
	o, err := h.restaurants.Change(r.Context(), user, restaurant, id, r.PathValue("action"))
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, 200, toDTO(o))
}

type sagaDTO struct {
	OrderID         int64     `json:"order_id"`
	State           string    `json:"state"`
	Version         int64     `json:"version"`
	DeliveryID      int64     `json:"delivery_id,omitempty"`
	DeliveryVersion int64     `json:"delivery_version"`
	CourierID       int64     `json:"courier_id,omitempty"`
	Attempts        int       `json:"attempts"`
	Deadline        time.Time `json:"deadline"`
	NextAttempt     time.Time `json:"next_attempt_at"`
	Reason          string    `json:"reason,omitempty"`
	CancelReason    string    `json:"cancellation_reason,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func toSagaDTO(s domain.Saga) sagaDTO {
	return sagaDTO{s.OrderID, s.State, s.Version, s.DeliveryID, s.DeliveryVersion, s.CourierID, s.Attempts, s.Deadline, s.NextAttempt, s.Reason, s.CancelReason, s.UpdatedAt}
}

func (h *handler) restaurantIdentity(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		return p.UserID, true
	}
	if !h.devAuth {
		h.writeError(w, 401, "authentication required")
		return 0, false
	}
	values := r.Header.Values("X-Dev-User-ID")
	if len(values) != 1 {
		h.writeError(w, 401, "identity required")
		return 0, false
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || id <= 0 {
		h.writeError(w, 401, "invalid identity")
		return 0, false
	}
	return id, true
}
