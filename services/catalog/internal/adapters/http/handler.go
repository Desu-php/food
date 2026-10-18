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

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

const maxBodyBytes = 64 << 10

type handler struct {
	restaurants *application.Restaurants
	menu        *application.Menu
	ready       func(context.Context) error
	logger      *slog.Logger
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

type restaurantDTO struct {
	Latitude  *float64  `json:"latitude,omitempty"`
	Longitude *float64  `json:"longitude,omitempty"`
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address,omitempty"`
	IsOpen    bool      `json:"is_open"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toDTO(r domain.Restaurant) restaurantDTO {
	return restaurantDTO{r.Latitude, r.Longitude, r.ID, r.Name, r.Address, r.IsOpen, r.CreatedAt.UTC(), r.UpdatedAt.UTC()}
}

// RawMessage distinguishes omitted fields from explicit null and false values.
type mutationDTO struct {
	Latitude  json.RawMessage `json:"latitude"`
	Longitude json.RawMessage `json:"longitude"`
	Name      json.RawMessage `json:"name"`
	Address   json.RawMessage `json:"address"`
	IsOpen    json.RawMessage `json:"is_open"`
}

func (d mutationDTO) changes() (domain.RestaurantChanges, error) {
	var c domain.RestaurantChanges
	for _, field := range []struct {
		raw    json.RawMessage
		target **float64
	}{{d.Latitude, &c.Latitude}, {d.Longitude, &c.Longitude}} {
		if len(field.raw) > 0 {
			if err := json.Unmarshal(field.raw, field.target); err != nil || *field.target == nil {
				return c, domain.ErrCoordinates
			}
		}
	}
	if len(d.Address) > 0 {
		var v *string
		if err := json.Unmarshal(d.Address, &v); err != nil || v == nil {
			return c, errors.New("address must be a string")
		}
		c.Address = v
	}
	if len(d.Name) != 0 {
		var name *string
		if err := json.Unmarshal(d.Name, &name); err != nil || name == nil {
			return c, errors.New("name must be a string")
		}
		c.Name = name
	}
	if len(d.IsOpen) != 0 {
		var isOpen *bool
		if err := json.Unmarshal(d.IsOpen, &isOpen); err != nil || isOpen == nil {
			return c, errors.New("is_open must be a boolean")
		}
		c.IsOpen = isOpen
	}
	return c, nil
}

func NewHandler(restaurants *application.Restaurants, menu *application.Menu, ready func(context.Context) error, logger *slog.Logger, timeout time.Duration) http.Handler {
	h := &handler{restaurants: restaurants, menu: menu, ready: ready, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", h.readiness)
	mux.HandleFunc("GET /restaurants", h.list)
	mux.HandleFunc("GET /restaurants/{id}", h.get)
	h.registerPublicMenuRoutes(mux)
	h.registerAdministrativeMenuRoutes(mux)
	mux.HandleFunc("POST /restaurants", h.create)
	mux.HandleFunc("PATCH /restaurants/{id}", h.update)
	mux.HandleFunc("/restaurants", h.methodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc("/restaurants/{id}", h.methodNotAllowed("GET, HEAD, PATCH"))
	mux.HandleFunc("/health", h.methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/ready", h.methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h.writeError(w, http.StatusNotFound, "endpoint not found")
	})
	return h.withMiddleware(mux, timeout)
}

func (h *handler) withMiddleware(next http.Handler, timeout time.Duration) http.Handler {
	logger := h.logger
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := &responseWriter{ResponseWriter: w}
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("request panic", "method", r.Method, "path", r.URL.Path)
				if response.status == 0 {
					h.writeError(response, http.StatusInternalServerError, "internal server error")
				}
			}
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(started))
		}()
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(response, r.WithContext(ctx))
	})
}

func (h *handler) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *handler) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.ready(ctx); err != nil {
		h.logger.Warn("readiness check failed")
		h.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page := application.Page{Limit: 20}
	query := r.URL.Query()
	for _, param := range []struct {
		key    string
		target *int
	}{{"limit", &page.Limit}, {"offset", &page.Offset}} {
		values, present := query[param.key]
		if !present {
			continue
		}
		if len(values) != 1 {
			h.handleError(w, application.ErrInvalidPagination)
			return
		}
		value, err := strconv.Atoi(values[0])
		if err != nil {
			h.handleError(w, application.ErrInvalidPagination)
			return
		}
		*param.target = value
	}
	restaurants, err := h.restaurants.List(r.Context(), page)
	if err != nil {
		h.handleError(w, err)
		return
	}
	result := make([]restaurantDTO, 0, len(restaurants))
	for _, restaurant := range restaurants {
		result = append(result, toDTO(restaurant))
	}
	h.writeJSON(w, http.StatusOK, result)
}

func restaurantID(r *http.Request) (int64, error) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return 0, domain.ErrInvalidID
	}
	return int64(id), nil
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := restaurantID(r)
	if err != nil {
		h.handleError(w, err)
		return
	}
	restaurant, err := h.restaurants.Get(r.Context(), id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, toDTO(restaurant))
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

func (h *handler) decode(w http.ResponseWriter, r *http.Request) (domain.RestaurantChanges, bool) {
	var input mutationDTO
	if !h.decodeJSON(w, r, &input) {
		return domain.RestaurantChanges{}, false
	}
	changes, err := input.changes()
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return domain.RestaurantChanges{}, false
	}
	return changes, true
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, 0) {
		return
	}
	input, ok := h.decode(w, r)
	if !ok {
		return
	}
	if input.Latitude != nil || input.Longitude != nil {
		h.writeError(w, 400, "set coordinates through PATCH /restaurants/{id}")
		return
	}
	var name string
	var isOpen bool
	if input.Name != nil {
		name = *input.Name
	}
	if input.IsOpen != nil {
		isOpen = *input.IsOpen
	}
	address := ""
	if input.Address != nil {
		address = *input.Address
		if _, err := domain.NormalizeAddress(address); err != nil {
			h.handleError(w, err)
			return
		}
	}
	restaurant, err := h.restaurants.Create(r.Context(), name, isOpen, address)
	if err != nil {
		h.handleError(w, err)
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/restaurants/%d", restaurant.ID))
	h.writeJSON(w, http.StatusCreated, toDTO(restaurant))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	grantID, _ := restaurantID(r)
	if !h.authorize(w, r, grantID) {
		return
	}
	id, err := restaurantID(r)
	if err != nil {
		h.handleError(w, err)
		return
	}
	changes, ok := h.decode(w, r)
	if !ok {
		return
	}
	restaurant, err := h.restaurants.Update(r.Context(), id, changes)
	if err != nil {
		h.handleError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, toDTO(restaurant))
}

func (h *handler) handleError(w http.ResponseWriter, err error) {
	for _, validation := range []error{domain.ErrCoordinates, domain.ErrInvalidMenuID, domain.ErrCategoryRestaurantMismatch, domain.ErrInvalidPrice, domain.ErrInvalidCurrency, domain.ErrInvalidSortOrder, domain.ErrInvalidDescription, domain.ErrEmptyMenuUpdate} {
		if errors.Is(err, validation) {
			h.writeError(w, http.StatusBadRequest, validation.Error())
			return
		}
	}
	switch {
	case errors.Is(err, domain.ErrCategoryNotFound):
		h.writeError(w, http.StatusNotFound, domain.ErrCategoryNotFound.Error())
	case errors.Is(err, domain.ErrItemNotFound):
		h.writeError(w, http.StatusNotFound, domain.ErrItemNotFound.Error())
	case errors.Is(err, domain.ErrNotFound):
		h.writeError(w, http.StatusNotFound, domain.ErrNotFound.Error())
	case errors.Is(err, domain.ErrInvalidID):
		h.writeError(w, http.StatusBadRequest, domain.ErrInvalidID.Error())
	case errors.Is(err, domain.ErrInvalidAddress):
		h.writeError(w, 400, domain.ErrInvalidAddress.Error())
	case errors.Is(err, domain.ErrInvalidName):
		h.writeError(w, http.StatusBadRequest, domain.ErrInvalidName.Error())
	case errors.Is(err, domain.ErrEmptyUpdate):
		h.writeError(w, http.StatusBadRequest, domain.ErrEmptyUpdate.Error())
	case errors.Is(err, application.ErrInvalidPagination):
		h.writeError(w, http.StatusBadRequest, application.ErrInvalidPagination.Error())
	case errors.Is(err, context.DeadlineExceeded):
		h.writeError(w, http.StatusGatewayTimeout, "request timed out")
	case errors.Is(err, context.Canceled):
		h.writeError(w, http.StatusRequestTimeout, "request canceled")
	default:
		// Do not expose SQL, connection strings or internal error details to clients.
		h.logger.Error("restaurant operation failed", "error_type", fmt.Sprintf("%T", err))
		h.writeError(w, http.StatusInternalServerError, "internal server error")
	}
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

func (h *handler) authorize(w http.ResponseWriter, r *http.Request, restaurant int64) bool {
	p, ok := security.FromContext(r.Context())
	if !ok {
		h.writeError(w, 401, "authentication required")
		return false
	}
	if p.Has("admin") || (restaurant > 0 && p.Has("restaurant_staff") && p.Restaurant(restaurant)) {
		return true
	}
	h.writeError(w, 403, "restaurant access denied")
	return false
}
