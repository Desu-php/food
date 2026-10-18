package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

const maxBodyBytes = 64 << 10

type handler struct {
	restaurants *application.Restaurants
	orders      *application.Orders
	ready       func(context.Context) error
	logger      *slog.Logger
	devAuth     bool
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type orderDTO struct {
	DeliveryLatitude      *float64                `json:"delivery_latitude,omitempty"`
	DeliveryLongitude     *float64                `json:"delivery_longitude,omitempty"`
	PricingSnapshot       *domain.PricingSnapshot `json:"pricing_snapshot,omitempty"`
	TotalMinor            int64                   `json:"total_minor,omitempty"`
	FulfillmentType       domain.FulfillmentType  `json:"fulfillment_type"`
	DeliveryAddress       string                  `json:"delivery_address,omitempty"`
	PickupAddress         string                  `json:"pickup_address,omitempty"`
	DeliveryFeeMinor      int64                   `json:"delivery_fee_minor"`
	ID                    int64                   `json:"id"`
	CustomerID            int64                   `json:"customer_id"`
	RestaurantID          int64                   `json:"restaurant_id"`
	Status                domain.Status           `json:"status"`
	CourierID             int64                   `json:"courier_id,omitempty"`
	CancellationRequested bool                    `json:"cancellation_requested"`
	PaymentMethod         string                  `json:"payment_method"`
	PaymentStatus         string                  `json:"payment_status"`
	Currency              string                  `json:"currency"`
	SubtotalMinor         int64                   `json:"subtotal_minor"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	Items                 []itemDTO               `json:"items"`
}
type itemDTO struct {
	ID              int64  `json:"id"`
	OrderID         int64  `json:"order_id"`
	MenuItemID      int64  `json:"menu_item_id"`
	Name            string `json:"name"`
	Quantity        int64  `json:"quantity"`
	UnitPriceMinor  int64  `json:"unit_price_minor"`
	TotalPriceMinor int64  `json:"total_price_minor"`
}

func toDTO(order domain.Order) orderDTO {
	s := order.Snapshot()
	var latitude, longitude *float64
	if s.DeliveryLocation != nil {
		latitude = &s.DeliveryLocation.Latitude
		longitude = &s.DeliveryLocation.Longitude
	}
	result := orderDTO{latitude, longitude, s.PricingSnapshot, s.TotalMinor, s.FulfillmentType, s.DeliveryAddress, s.PickupAddress, s.DeliveryFeeMinor, s.ID, s.CustomerID, s.RestaurantID, s.Status, s.CourierID, s.CancellationRequested, s.PaymentMethod, s.PaymentStatus, s.Currency, s.SubtotalMinor, s.CreatedAt.UTC(), s.UpdatedAt.UTC(), make([]itemDTO, 0, len(s.Items))}
	for _, p := range s.Items {
		result.Items = append(result.Items, itemDTO{p.ID, p.OrderID, p.MenuItemID, p.Name, p.Quantity, p.UnitPriceMinor, p.TotalPriceMinor})
	}
	return result
}

// devAuth is deliberately opt-in. Bootstrap rejects its use outside dev/test.
func NewHandler(orders *application.Orders, ready func(context.Context) error, logger *slog.Logger, timeout time.Duration, devAuth bool, restaurants ...*application.Restaurants) http.Handler {
	h := &handler{orders: orders, ready: ready, logger: logger, devAuth: devAuth}
	if len(restaurants) > 0 {
		h.restaurants = restaurants[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /restaurants/{restaurant}/orders", h.restaurantAction)
	mux.HandleFunc("GET /restaurants/{restaurant}/orders/{id}/saga", h.restaurantAction)
	mux.HandleFunc("POST /restaurants/{restaurant}/orders/{id}/{action}", h.restaurantAction)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { h.writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if h.ready(ctx) != nil {
			h.writeError(w, 503, "database unavailable")
			return
		}
		h.writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /orders/{id}/pickup-code", h.pickupCode)
	mux.HandleFunc("POST /orders", h.create)
	mux.HandleFunc("GET /orders/{id}", h.get)
	mux.HandleFunc("POST /orders/{id}/cancel", h.cancel)
	for _, route := range []struct{ pattern, allow string }{{"/orders", "POST"}, {"/orders/{id}", "GET, HEAD"}, {"/orders/{id}/cancel", "POST"}, {"/health", "GET, HEAD"}, {"/ready", "GET, HEAD"}} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", route.allow)
			h.writeError(w, 405, "method not allowed")
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { h.writeError(w, 404, "endpoint not found") })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !correlation.Valid(id) {
			id = events.NewID()
		}
		r = r.WithContext(correlation.WithID(r.Context(), id))
		w.Header().Set("X-Request-ID", id)
		response := &responseWriter{ResponseWriter: w}
		started := time.Now()
		defer func() {
			if recover() != nil {
				logger.Error("request panic")
				if response.status == 0 {
					h.writeError(response, 500, "internal server error")
				}
			}
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", response.status, "duration", time.Since(started), "correlation_id", id)
		}()
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		mux.ServeHTTP(response, r.WithContext(ctx))
	})
}

func (h *handler) customer(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		if p.Has("customer") && p.CustomerID > 0 {
			return p.CustomerID, true
		}
		h.writeError(w, 403, "customer access denied")
		return 0, false
	}

	if !h.devAuth {
		h.writeError(w, 401, "authentication required; production identity integration is not configured")
		return 0, false
	}
	values := r.Header.Values("X-Dev-Customer-ID")
	if len(values) != 1 {
		h.writeError(w, 401, "X-Dev-Customer-ID is required in dev/test mode")
		return 0, false
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || id <= 0 {
		h.writeError(w, 401, "invalid dev customer ID")
		return 0, false
	}
	return id, true
}
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	customer, ok := h.customer(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedTotalMinor    *int64                      `json:"expected_total_minor"`
		DeliveryLatitude      *float64                    `json:"delivery_latitude"`
		DeliveryLongitude     *float64                    `json:"delivery_longitude"`
		FulfillmentType       domain.FulfillmentType      `json:"fulfillment_type"`
		DeliveryAddress       string                      `json:"delivery_address"`
		RestaurantID          int64                       `json:"restaurant_id"`
		Currency              string                      `json:"currency"`
		ExpectedSubtotalMinor int64                       `json:"expected_subtotal_minor"`
		Items                 []application.RequestedItem `json:"items"`
	}
	if !h.decodeJSON(w, r, &input) {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		h.handleError(w, application.ErrInvalidKey)
		return
	}
	if input.Currency == "" {
		input.Currency = domain.CurrencyTJS
	}
	order, err := h.orders.CreateOrder(r.Context(), application.CreateCommand{ExpectedTotalMinor: input.ExpectedTotalMinor, DeliveryLatitude: input.DeliveryLatitude, DeliveryLongitude: input.DeliveryLongitude, FulfillmentType: input.FulfillmentType, DeliveryAddress: input.DeliveryAddress, CustomerID: customer, RestaurantID: input.RestaurantID, Currency: input.Currency, ExpectedSubtotalMinor: input.ExpectedSubtotalMinor, Items: input.Items}, keys[0])
	if err != nil {
		h.handleError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/orders/%d", order.Snapshot().ID))
	h.writeJSON(w, 201, toDTO(order))
}
func (h *handler) readIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	customer, ok := h.customer(w, r)
	if !ok {
		return 0, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.handleError(w, application.ErrInvalidRequest)
		return 0, 0, false
	}
	return customer, id, true
}
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	customer, id, ok := h.readIDs(w, r)
	if !ok {
		return
	}
	order, err := h.orders.GetOrder(r.Context(), customer, id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, 200, toDTO(order))
}
func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	customer, id, ok := h.readIDs(w, r)
	if !ok {
		return
	}
	// This action has no client-controlled fields.
	body := http.MaxBytesReader(w, r.Body, 1)
	data, err := io.ReadAll(body)
	if err != nil || len(data) > 0 {
		h.writeError(w, 400, "cancel request body must be empty")
		return
	}
	order, err := h.orders.CancelOrder(r.Context(), customer, id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, 200, toDTO(order))
}
func (h *handler) handleError(w http.ResponseWriter, err error) {
	var changed *application.PriceChangedError
	if errors.As(err, &changed) {
		h.writeJSON(w, 409, struct {
			Error   string                         `json:"error"`
			Code    string                         `json:"code"`
			Current *application.PriceChangedError `json:"current_quote"`
		}{changed.Error(), "price_changed", changed})
		return
	}
	for _, entry := range []struct {
		err    error
		status int
	}{
		{application.ErrPickupCode, 403}, {application.ErrPickupRateLimited, 429}, {domain.ErrPaymentMismatch, 409}, {application.ErrInvalidKey, 400}, {application.ErrInvalidRequest, 400}, {domain.ErrInvalidOrder, 400}, {domain.ErrInvalidItem, 400}, {domain.ErrRestaurantMismatch, 400}, {domain.ErrOverflow, 400},
		{application.ErrForbidden, 403}, {domain.ErrSagaTransition, 409}, {domain.ErrInvalidTransition, 409}, {domain.ErrNotFound, 404}, {application.ErrRestaurantNotFound, 404}, {application.ErrItemNotFound, 404},
		{application.ErrFulfillmentUnavailable, 409}, {application.ErrPriceChanged, 409}, {application.ErrIdempotencyConflict, 409}, {domain.ErrCannotCancel, 409}, {application.ErrRestaurantUnavailable, 409}, {application.ErrItemUnavailable, 409}, {application.ErrItemRestaurantMismatch, 409},
		{application.ErrCatalogUnavailable, 503},
	} {
		if errors.Is(err, entry.err) {
			h.writeError(w, entry.status, entry.err.Error())
			return
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		h.writeError(w, 504, "request timed out")
		return
	}
	if errors.Is(err, context.Canceled) {
		h.writeError(w, 408, "request canceled")
		return
	}
	h.logger.Error("order operation failed", "error_type", fmt.Sprintf("%T", err))
	h.writeError(w, 500, "internal server error")
}

func (h *handler) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		h.writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil {
		if extraErr := decoder.Decode(&struct{}{}); extraErr != io.EOF {
			if extraErr == nil {
				extraErr = errors.New("multiple JSON values")
			}
			err = extraErr
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			h.writeError(w, http.StatusBadRequest, "invalid JSON or unknown fields")
		}
		return false
	}
	return true
}

func (h *handler) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, map[string]string{"error": message})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		h.logger.Error("encode response failed")
		status = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		h.logger.Warn("write response failed")
	}
}

func (h *handler) pickupCode(w http.ResponseWriter, r *http.Request) {
	customer, id, ok := h.readIDs(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(data) > 0 {
		h.writeError(w, 400, "pickup-code body must be empty")
		return
	}
	code, err := h.orders.IssuePickupCode(r.Context(), customer, id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, 200, code)
}
