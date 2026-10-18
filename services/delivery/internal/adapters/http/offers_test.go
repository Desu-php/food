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
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type offerStore struct {
	application.OfferRepository
	courier, id int64
	accept      bool
}

func (s *offerStore) Offers(_ context.Context, courier int64) ([]domain.CourierOffer, error) {
	s.courier = courier
	return []domain.CourierOffer{}, nil
}
func (s *offerStore) RespondOffer(_ context.Context, courier, id int64, accept bool) (domain.CourierOffer, error) {
	s.courier, s.id, s.accept = courier, id, accept
	return domain.CourierOffer{ID: id, CourierID: courier}, nil
}
func TestOfferHTTPAuthenticationAndServerIdentity(t *testing.T) {
	store := &offerStore{}
	s := application.NewDelivery(nil, nil).WithOffers(application.NewOffers(store))
	h := NewHandler(s, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	courier := security.Principal{Subject: "oidc-courier", CourierID: 7, Roles: []string{"courier"}}
	for _, tc := range []struct {
		method, path, body string
		principal          security.Principal
		status             int
	}{
		{"GET", "/couriers/me/offers", "", security.Principal{}, 401},
		{"POST", "/courier-offers/1/accept", "", security.Principal{}, 401},
		{"POST", "/courier-offers/1/decline", "", security.Principal{}, 401},
		{"GET", "/couriers/me/offers", "", security.Principal{Subject: "staff", Roles: []string{"dispatcher"}}, 403},
		{"GET", "/couriers/me/offers", "", courier, 200},
		{"POST", "/courier-offers/5/accept", "", courier, 200},
		{"POST", "/courier-offers/5/decline", "", courier, 200},
		{"POST", "/courier-offers/0/accept", "", courier, 400},
		{"POST", "/courier-offers/5/accept", `{"courier_id":99}`, courier, 400},
		{"GET", "/courier-offers/5/accept", "", courier, 405},
	} {
		store.courier = 0
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Dev-Courier-ID", "99")
		req = req.WithContext(security.WithPrincipal(req.Context(), tc.principal))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatal(tc.path, res.Code, res.Body)
		}
		if tc.status == 200 && store.courier != 7 {
			t.Fatal("client identity used", store.courier)
		}
		if tc.status != 200 && store.courier != 0 {
			t.Fatal("invalid request reached storage")
		}
		if tc.status == 200 && tc.method == "POST" && (store.id != 5 || store.accept != strings.HasSuffix(tc.path, "/accept")) {
			t.Fatal(store)
		}
	}
}
