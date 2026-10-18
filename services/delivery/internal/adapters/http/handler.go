package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type handler struct {
	service        *application.Delivery
	logger         *slog.Logger
	devAuth        bool
	trackingConfig TrackingConfig
	connections    trackingConnections
}

func NewHandler(service *application.Delivery, ready func(context.Context) error, logger *slog.Logger, timeout time.Duration, devAuth bool, tracking ...TrackingConfig) http.Handler {
	cfg := DefaultTrackingConfig()
	if len(tracking) > 0 {
		cfg = tracking[0]
	}
	h := &handler{service: service, logger: logger, devAuth: devAuth, trackingConfig: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}/tracking", h.tracking)
	mux.HandleFunc("/orders/{id}/tracking", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if ready(r.Context()) != nil {
			writeJSON(w, 503, map[string]string{"error": "database unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /couriers/me/offers", h.offers)
	mux.HandleFunc("POST /courier-offers/{id}/accept", h.respondOffer)
	mux.HandleFunc("POST /courier-offers/{id}/decline", h.respondOffer)
	mux.HandleFunc("POST /couriers/me/location", h.location)
	mux.HandleFunc("POST /deliveries/{id}/dispatch/retry", h.retryDispatch)
	mux.HandleFunc("POST /couriers", h.createCourier)
	mux.HandleFunc("POST /couriers/{id}/status", h.setAvailability)
	mux.HandleFunc("POST /deliveries/{id}/decline", h.decline)
	mux.HandleFunc("GET /couriers", h.couriers)
	mux.HandleFunc("GET /deliveries", h.list)
	mux.HandleFunc("GET /deliveries/{id}", h.get)
	mux.HandleFunc("POST /deliveries/{id}/assign", h.assign)
	mux.HandleFunc("POST /deliveries/{id}/pickup", h.pickup)
	mux.HandleFunc("POST /deliveries/{id}/complete", h.complete)
	for _, route := range []struct{ pattern, allow string }{{"/couriers/me/offers", "GET, HEAD"}, {"/courier-offers/{id}/accept", "POST"}, {"/courier-offers/{id}/decline", "POST"}, {"/couriers/me/location", "POST"}, {"/deliveries/{id}/dispatch/retry", "POST"}, {"/health", "GET, HEAD"}, {"/ready", "GET, HEAD"}, {"/couriers", "GET, HEAD, POST"}, {"/couriers/{id}/status", "POST"}, {"/deliveries", "GET, HEAD"}, {"/deliveries/{id}", "GET, HEAD"}, {"/deliveries/{id}/assign", "POST"}, {"/deliveries/{id}/decline", "POST"}, {"/deliveries/{id}/pickup", "POST"}, {"/deliveries/{id}/complete", "POST"}} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", route.allow)
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "endpoint not found"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !correlation.Valid(id) {
			id = events.NewID()
		}
		w.Header().Set("X-Request-ID", id)
		requestTimeout := timeout
		if _, pattern := mux.Handler(r); pattern == "GET /orders/{id}/tracking" {
			requestTimeout = cfg.MaxDuration
		}
		ctx, cancel := context.WithTimeout(correlation.WithID(r.Context(), id), requestTimeout)
		defer cancel()
		defer func() {
			if recover() != nil {
				logger.Error("delivery request panic", "correlation_id", id)
				writeJSON(w, 500, map[string]string{"error": "internal server error"})
			}
		}()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status = 500
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
func (h *handler) fail(w http.ResponseWriter, err error) {
	status := 500
	message := "internal server error"
	for _, v := range []struct {
		err    error
		status int
	}{{domain.ErrStaleLocation, 409}, {application.ErrLocationRate, 429}, {application.ErrLocationUnavailable, 503}, {domain.ErrInvalid, 400}, {domain.ErrForbidden, 403}, {domain.ErrNotFound, 404}, {domain.ErrConflict, 409}, {domain.ErrTransition, 409}, {domain.ErrPayment, 400}, {domain.ErrCancellationUnsafe, 409}} {
		if errors.Is(err, v.err) {
			status = v.status
			message = v.err.Error()
			break
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = 504
		message = "request timed out"
	}
	if status == 500 {
		h.logger.Error("delivery operation failed", "error_type", "storage_or_internal")
	}
	writeJSON(w, status, map[string]string{"error": message})
}
func (h *handler) identity(w http.ResponseWriter, r *http.Request, courier bool) (int64, bool) {
	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		if courier && p.Has("courier") && p.CourierID > 0 {
			return p.CourierID, true
		}
		if !courier && (p.Has("dispatcher") || p.Has("admin")) && p.Can("delivery:dispatch") {
			return p.UserID, true
		}
		writeJSON(w, 403, map[string]string{"error": "access denied"})
		return 0, false
	}

	if !h.devAuth {
		writeJSON(w, 401, map[string]string{"error": "authentication required"})
		return 0, false
	}
	header := "X-Dev-User-ID"
	if courier {
		header = "X-Dev-Courier-ID"
	}
	v := r.Header.Values(header)
	if len(v) != 1 {
		writeJSON(w, 401, map[string]string{"error": "identity required"})
		return 0, false
	}
	id, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, 401, map[string]string{"error": "invalid identity"})
		return 0, false
	}
	return id, true
}
func (h *handler) reader(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		if (p.Has("dispatcher") || p.Has("admin")) && p.Can("delivery:dispatch") {
			return p.UserID, 0, true
		}
		id, allowed := h.identity(w, r, true)
		return 0, id, allowed
	}

	// Ambiguous identities are rejected; a courier identity cannot grant dispatcher access.
	if r.Header.Get("X-Dev-Courier-ID") != "" {
		if r.Header.Get("X-Dev-User-ID") != "" {
			writeJSON(w, 401, map[string]string{"error": "ambiguous identity"})
			return 0, 0, false
		}
		id, ok := h.identity(w, r, true)
		return 0, id, ok
	}
	id, ok := h.identity(w, r, false)
	return id, 0, ok
}
func (h *handler) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeJSON(w, 415, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err = dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = domain.ErrInvalid
	}
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeJSON(w, 413, map[string]string{"error": "request body too large"})
			return false
		}
		writeJSON(w, 400, map[string]string{"error": "invalid JSON or unknown fields"})
		return false
	}
	return true
}
func id(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || v <= 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid ID"})
		return 0, false
	}
	return v, true
}
func page(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	after := int64(0)
	limit := 20
	var err error
	if v := r.URL.Query().Get("after"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid pagination"})
			return 0, 0, false
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid pagination"})
			return 0, 0, false
		}
	}
	return after, limit, true
}
func (h *handler) createCourier(w http.ResponseWriter, r *http.Request) {
	user, ok := h.identity(w, r, false)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	c, err := h.service.CreateCourier(r.Context(), user, input.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location", "/couriers/"+strconv.FormatInt(c.ID, 10))
	writeJSON(w, 201, toCourier(c))
}
func (h *handler) couriers(w http.ResponseWriter, r *http.Request) {
	user, ok := h.identity(w, r, false)
	if !ok {
		return
	}
	after, limit, ok := page(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "AVAILABLE" {
		h.fail(w, domain.ErrInvalid)
		return
	}
	c, err := h.service.Couriers(r.Context(), user, after, limit, status == "AVAILABLE")
	if err != nil {
		h.fail(w, err)
		return
	}
	result := make([]courierDTO, 0, len(c))
	for _, v := range c {
		result = append(result, toCourier(v))
	}
	writeJSON(w, 200, result)
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	user, courier, ok := h.reader(w, r)
	if !ok {
		return
	}
	after, limit, ok := page(w, r)
	if !ok {
		return
	}
	d, err := h.service.List(r.Context(), user, courier, after, limit)
	if err != nil {
		h.fail(w, err)
		return
	}
	result := make([]deliveryDTO, 0, len(d))
	for _, v := range d {
		result = append(result, toDelivery(v))
	}
	writeJSON(w, 200, result)
}
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	user, courier, ok := h.reader(w, r)
	if !ok {
		return
	}
	delivery, ok := id(w, r)
	if !ok {
		return
	}
	d, err := h.service.Get(r.Context(), user, courier, delivery)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toDelivery(d))
}
func (h *handler) assign(w http.ResponseWriter, r *http.Request) {
	user, ok := h.identity(w, r, false)
	if !ok {
		return
	}
	delivery, ok := id(w, r)
	if !ok {
		return
	}
	var input struct {
		CourierID int64 `json:"courier_id"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	d, err := h.service.Assign(r.Context(), user, delivery, input.CourierID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toDelivery(d))
}
func (h *handler) pickup(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	delivery, ok := id(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) > 0 {
		writeJSON(w, 400, map[string]string{"error": "pickup body must be empty"})
		return
	}
	d, err := h.service.Pickup(r.Context(), courier, delivery)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toDelivery(d))
}
func (h *handler) complete(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	delivery, ok := id(w, r)
	if !ok {
		return
	}
	var input struct {
		PaymentConfirmed bool   `json:"payment_confirmed"`
		POSReference     string `json:"pos_reference"`
		ReceiptReference string `json:"receipt_reference"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	if input.ReceiptReference != "" && input.POSReference != "" && input.ReceiptReference != input.POSReference {
		h.fail(w, domain.ErrInvalid)
		return
	}
	reference := input.ReceiptReference
	if reference == "" {
		reference = input.POSReference
	}
	d, err := h.service.Complete(r.Context(), courier, delivery, input.PaymentConfirmed, reference)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toDelivery(d))
}

func (h *handler) setAvailability(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
	if !ok {
		return
	}
	courierID, ok := id(w, r)
	if !ok {
		return
	}
	var input struct {
		Status string `json:"status"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	c, err := h.service.SetAvailability(r.Context(), courier, courierID, input.Status)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toCourier(c))
}
func (h *handler) decline(w http.ResponseWriter, r *http.Request) {
	courier, ok := h.identity(w, r, true)
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
	d, err := h.service.Decline(r.Context(), courier, delivery)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, 200, toDelivery(d))
}
