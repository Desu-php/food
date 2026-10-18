package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/food/contracts/geo"
	"example.com/food/delivery/internal/application"
	"example.com/food/platform/security"
)

type locationRepo struct {
	application.DispatchRepository
	courier int64
}

func (r *locationRepo) CourierAvailable(_ context.Context, id int64) error {
	r.courier = id
	return nil
}

type locationSink struct {
	application.Locations
	courier int64
	err     error
}

func (s *locationSink) Update(_ context.Context, id int64, p geo.Point) error {
	s.courier = id
	return s.err
}
func TestLocationUsesServerMappedCourierAndRateLimit(t *testing.T) {
	repo := &locationRepo{}
	sink := &locationSink{}
	dispatch := application.NewDispatch(repo, sink, application.DefaultDispatchConfig())
	service := application.NewDelivery(nil, []int64{20}).WithDispatch(dispatch)
	h := NewHandler(service, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	p := security.Principal{Subject: "oidc-subject", UserID: 100, CourierID: 7, Roles: []string{"courier"}}
	for _, tc := range []struct {
		body string
		err  error
		code int
	}{
		{`{"latitude":38.56,"longitude":68.77}`, nil, 204},
		{`{"latitude":38.56,"longitude":68.77}`, application.ErrLocationRate, 429},
		{`{"latitude":38.56,"longitude":68.77,"courier_id":99}`, nil, 400},
	} {
		sink.err = tc.err
		sink.courier = 0
		req := httptest.NewRequest("POST", "/couriers/me/location", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dev-Courier-ID", "99")
		req = req.WithContext(security.WithPrincipal(req.Context(), p))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatal(res.Code, res.Body)
		}
		if tc.code != 400 && (repo.courier != 7 || sink.courier != 7) {
			t.Fatal("untrusted ID used", repo.courier, sink.courier)
		}
		if tc.code == 400 && sink.courier != 0 {
			t.Fatal("spoofed ID accepted")
		}
	}
}
