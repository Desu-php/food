package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/orders/internal/application"
)

func TestOIDCIdentityMappingAndOrderOwnership(t *testing.T) {
	repo := &testRepository{}
	h := newHandler(repo, nil, false, nil)
	call := func(p security.Principal, method, path, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "key")
		r = r.WithContext(security.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	customer := security.Principal{Subject: "external-user", UserID: 100, CustomerID: 7, Roles: []string{"customer"}}
	if status := call(customer, "POST", "/orders", createBody); status != 201 {
		t.Fatal("mapped customer cannot create", status)
	}
	if status := call(customer, "GET", "/orders/1", ""); status != 200 {
		t.Fatal(status)
	}
	customer.Subject = "another-user"
	customer.CustomerID = 8
	if status := call(customer, "GET", "/orders/1", ""); status != 404 {
		t.Fatal("foreign order disclosed", status)
	}
	customer.Roles = []string{"courier"}
	if status := call(customer, "POST", "/orders", createBody); status != 403 {
		t.Fatal("wrong role accepted", status)
	}
}

func TestRestaurantAndOperatorResourceGrants(t *testing.T) {
	h := NewHandler(nil, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false, application.NewRestaurants(nil, map[int64][]int64{10: {1}}))
	for _, tc := range []struct {
		path string
		p    security.Principal
	}{
		{"/restaurants/2/orders", security.Principal{Subject: "staff", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}},
		{"/restaurants/1/orders/1/retry", security.Principal{Subject: "staff", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}},
		{"/restaurants/1/orders/1/retry", security.Principal{Subject: "dispatcher", UserID: 10, Roles: []string{"dispatcher"}, Restaurants: []int64{1}}},
	} {
		method := "POST"
		if tc.path == "/restaurants/2/orders" {
			method = "GET"
		}
		r := httptest.NewRequest(method, tc.path, nil)
		r = r.WithContext(security.WithPrincipal(r.Context(), tc.p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(tc.path, w.Code)
		}
	}
}
