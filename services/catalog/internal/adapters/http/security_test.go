package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/catalog/internal/application"
)

func TestCatalogMutationsRequireResourceAuthorization(t *testing.T) {
	repo := &testRepository{nextID: 1}
	h := NewHandler(application.NewRestaurants(repo), nil, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	routes := []struct{ method, path string }{{"POST", "/restaurants"}, {"PATCH", "/restaurants/2"}, {"POST", "/restaurants/2/categories"}, {"PATCH", "/restaurants/2/categories/1"}, {"POST", "/restaurants/2/items"}, {"PATCH", "/restaurants/2/items/1"}, {"DELETE", "/restaurants/2/items/1"}}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, staff := range []bool{false, true} {
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				r.Header.Set("Content-Type", "application/json")
				want := 401
				if staff {
					r = r.WithContext(security.WithPrincipal(r.Context(), security.Principal{UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}))
					want = 403
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("got %d want %d", w.Code, want)
				}
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/restaurants", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("anonymous catalog read denied", w.Code)
	}
	r = httptest.NewRequest("POST", "/restaurants", strings.NewReader(`{"name":"Owned","is_open":true}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(security.WithPrincipal(r.Context(), security.Principal{UserID: 1, Roles: []string{"admin"}}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal("authorized admin rejected", w.Code, w.Body.String())
	}
	// Staff can update a granted resource after the admin creates it.
	r = httptest.NewRequest("PATCH", "/restaurants/1", strings.NewReader(`{"name":"Staff update"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(security.WithPrincipal(r.Context(), security.Principal{Subject: "staff", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("own restaurant grant rejected", w.Code, w.Body.String())
	}

}
