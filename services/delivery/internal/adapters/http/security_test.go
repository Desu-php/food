package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/food/delivery/internal/application"
	"example.com/food/platform/security"
)

func TestMappedCourierCannotChangeOtherCourier(t *testing.T) {
	h := NewHandler(application.NewDelivery(nil, []int64{20}), func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	for _, tc := range []struct {
		path, body string
		p          security.Principal
	}{
		{"/couriers/2/status", `{"status":"AVAILABLE"}`, security.Principal{Subject: "external-courier", UserID: 100, CourierID: 1, Roles: []string{"courier"}}},
		{"/deliveries/1/complete", `{"payment_confirmed":true}`, security.Principal{Subject: "external-staff", UserID: 100, Roles: []string{"restaurant_staff"}}},
		{"/deliveries/1/assign", `{"courier_id":1}`, security.Principal{Subject: "external-dispatcher", UserID: 20, Roles: []string{"dispatcher"}}},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(security.WithPrincipal(r.Context(), tc.p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(tc.path, w.Code, w.Body)
		}
	}
}
