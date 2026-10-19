package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

func DeviceHandler(devices *application.Devices, health http.Handler, metrics func(context.Context) (string, error), devEnabled ...bool) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", health)
	respond := func(w http.ResponseWriter, value any, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			status := 500
			message := "internal error"
			if errors.Is(err, domain.ErrInvalidDevice) {
				status = 400
				message = "invalid device"
			}
			if errors.Is(err, domain.ErrNotFound) {
				status = 404
				message = "not found"
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}
	guard := func(fn func(http.ResponseWriter, *http.Request, security.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, ok := security.FromContext(r.Context())
			if len(devEnabled) > 0 && devEnabled[0] {
				customerValues, courierValues := r.Header.Values("X-Dev-Customer-ID"), r.Header.Values("X-Dev-Courier-ID")
				if len(customerValues)+len(courierValues) > 0 {
					if len(customerValues)+len(courierValues) != 1 || r.Header.Get("X-Dev-User-ID") != "" {
						http.Error(w, "access denied", 401)
						return
					}
					raw := r.Header.Get("X-Dev-Customer-ID")
					kind := "customer"
					if raw == "" {
						raw = r.Header.Get("X-Dev-Courier-ID")
						kind = "courier"
					}
					id, err := strconv.ParseInt(raw, 10, 64)
					if err != nil || id <= 0 {
						http.Error(w, "access denied", 401)
						return
					}
					p = security.Principal{UserID: id, Roles: []string{kind}}
					if kind == "customer" {
						p.CustomerID = id
					} else {
						p.CourierID = id
					}
					ok = true
				}
			}
			if !ok || p.UserID <= 0 {
				http.Error(w, "authentication required", 401)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			fn(w, r.WithContext(ctx), p)
		}
	}
	mux.HandleFunc("POST /v1/devices", guard(func(w http.ResponseWriter, r *http.Request, p security.Principal) {
		var input struct {
			OwnerType      string `json:"owner_type"`
			RestaurantID   int64  `json:"restaurant_id"`
			Platform       string `json:"platform"`
			InstallationID string `json:"installation_id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			respond(w, nil, domain.ErrInvalidDevice)
			return
		}
		var owner int64
		switch input.OwnerType {
		case domain.Customer:
			if p.Has("customer") {
				owner = p.CustomerID
			}
		case domain.Courier:
			if p.Has("courier") {
				owner = p.CourierID
			}
		case domain.Restaurant:
			if p.Has("restaurant_staff") && p.Restaurant(input.RestaurantID) {
				owner = input.RestaurantID
			}
		}
		if owner <= 0 || (input.OwnerType != domain.Restaurant && input.RestaurantID != 0) {
			http.Error(w, "access denied", 403)
			return
		}
		result, err := devices.Register(r.Context(), domain.DeviceRegistration{OwnerType: input.OwnerType, OwnerID: owner, RegisteredBy: p.UserID, Platform: input.Platform, InstallationID: input.InstallationID})
		respond(w, result, err)
	}))
	mux.HandleFunc("GET /v1/devices", guard(func(w http.ResponseWriter, r *http.Request, p security.Principal) {
		result, err := devices.List(r.Context(), p.UserID)
		respond(w, result, err)
	}))
	mux.HandleFunc("DELETE /v1/devices/{id}", guard(func(w http.ResponseWriter, r *http.Request, p security.Principal) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			respond(w, nil, domain.ErrNotFound)
			return
		}
		err = devices.Deactivate(r.Context(), p.UserID, id)
		if err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(204)
	}))
	if metrics != nil {
		mux.HandleFunc("GET /metrics", guard(func(w http.ResponseWriter, r *http.Request, p security.Principal) {
			if !p.Has("admin") {
				http.Error(w, "access denied", 403)
				return
			}
			result, err := metrics(r.Context())
			if err != nil {
				http.Error(w, "unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = io.WriteString(w, result)
		}))
	}
	return mux
}
