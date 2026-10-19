package httpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

type deviceMemory struct{ devices []domain.DeviceRegistration }

func (r *deviceMemory) Register(_ context.Context, d domain.DeviceRegistration) (domain.DeviceRegistration, error) {
	d.ID = int64(len(r.devices) + 1)
	d.IsActive = true
	r.devices = append(r.devices, d)
	return d, nil
}
func (r *deviceMemory) Devices(_ context.Context, user int64) ([]domain.DeviceRegistration, error) {
	var result = []domain.DeviceRegistration{}
	for _, d := range r.devices {
		if d.RegisteredBy == user {
			result = append(result, d)
		}
	}
	return result, nil
}
func (r *deviceMemory) Deactivate(_ context.Context, user, id int64) error {
	for i, d := range r.devices {
		if d.ID == id && d.RegisteredBy == user {
			r.devices[i].IsActive = false
			return nil
		}
	}
	return domain.ErrNotFound
}
func TestDeviceHTTPIdentityAndRedaction(t *testing.T) {
	store := &deviceMemory{}
	handler := DeviceHandler(application.NewDevices(store), NewHandler(func(context.Context) error { return nil }), nil)
	customer := security.Principal{UserID: 8, CustomerID: 42, Roles: []string{"customer"}}
	courier := security.Principal{UserID: 9, CourierID: 77, Roles: []string{"courier"}}
	staff := security.Principal{UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{3}}
	request := func(method, path, body string, p *security.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if p != nil {
			req = req.WithContext(security.WithPrincipal(req.Context(), *p))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	valid := `{"owner_type":"CUSTOMER","platform":"IOS","installation_id":"private-firebase-installation"}`
	for _, tt := range []struct {
		name, body string
		p          *security.Principal
		status     int
	}{
		{"anonymous", valid, nil, 401}, {"customer", valid, &customer, 200},
		{"foreign_owner", strings.TrimSuffix(valid, "}") + `,"owner_id":99}`, &customer, 400},
		{"foreign_courier", strings.ReplaceAll(valid, "CUSTOMER", "COURIER"), &customer, 403},
		{"courier", strings.ReplaceAll(valid, "CUSTOMER", "COURIER"), &courier, 200},
		{"staff", `{"owner_type":"RESTAURANT","restaurant_id":3,"platform":"ANDROID","installation_id":"private-installation-staff"}`, &staff, 200},
		{"foreign_restaurant", `{"owner_type":"RESTAURANT","restaurant_id":4,"platform":"WEB","installation_id":"private-installation-staff"}`, &staff, 403},
		{"invalid_platform", strings.ReplaceAll(valid, "IOS", "OTHER"), &customer, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := request("POST", "/v1/devices", tt.body, tt.p)
			if w.Code != tt.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatal("installation leaked")
			}
		})
	}
	w := request("GET", "/v1/devices", "", &customer)
	var devices []domain.DeviceRegistration
	if err := json.Unmarshal(w.Body.Bytes(), &devices); err != nil || len(devices) != 1 || devices[0].OwnerID != 42 || strings.Contains(w.Body.String(), "installation_id") {
		t.Fatal(w.Body.String(), err)
	}
	if w = request("DELETE", "/v1/devices/1", "", &courier); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = request("DELETE", "/v1/devices/1", "", &customer); w.Code != 204 {
		t.Fatal(w.Code)
	}
}
func TestDeviceHTTPUsesOIDCBoundary(t *testing.T) {
	// Missing bearer, spoofed dev identity and unknown bearer cannot reach devices.
	auth, err := security.New(security.Config{Environment: "test", Issuer: "https://issuer.example", Audience: "notifications-api", JWKSURL: "https://issuer.example/jwks"})
	if err != nil {
		t.Fatal(err)
	}
	h := auth.Wrap(DeviceHandler(application.NewDevices(&deviceMemory{}), NewHandler(func(context.Context) error { return nil }), nil))
	for _, headers := range []map[string]string{{}, {"X-Dev-User-ID": "8"}, {"Authorization": "Bearer invalid"}} {
		req := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
