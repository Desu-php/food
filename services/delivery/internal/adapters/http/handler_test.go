package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/services/delivery/internal/application"
)

func TestAuthenticationAndUntrustedPaymentFields(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := func(context.Context) error { return nil }
	for _, tc := range []struct {
		path, header, body string
		enabled            bool
		status             int
	}{
		{"/deliveries/1/complete", "", `{"payment_confirmed":true}`, true, 401},
		{"/deliveries/1/complete", "X-Dev-Courier-ID", `{"payment_confirmed":true}`, false, 401},
		{"/deliveries/1/complete", "X-Dev-Courier-ID", `{"payment_confirmed":true,"amount_minor":1}`, true, 400},
		{"/deliveries/1/assign", "X-Dev-User-ID", `{"courier_id":1}`, true, 403},
	} {
		h := NewHandler(application.NewDelivery(nil, []int64{20}), ready, logger, time.Second, tc.enabled)
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		if tc.header != "" {
			req.Header.Set(tc.header, "99")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body)
		}
	}
}
