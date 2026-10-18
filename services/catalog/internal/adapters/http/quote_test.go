package httpadapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

type quoteRepository struct{ err error }

func (r quoteRepository) LoadQuote(context.Context, int64, []int64) (domain.Restaurant, []domain.MenuItem, error) {
	return domain.Restaurant{ID: 1, IsOpen: true}, []domain.MenuItem{{ID: 10, RestaurantID: 1, Name: "Плов", PriceMinor: 6500, Currency: "TJS", IsAvailable: true}}, r.err
}
func TestInternalQuoteAndPublicIsolation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range []struct {
		body   string
		err    error
		status int
		code   string
	}{
		{`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":2}]}`, nil, 200, ""},
		{`{"restaurant_id":1,"items":[]}`, nil, 400, "invalid_quote"},
		{`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":2}]}`, domain.ErrNotFound, 404, "restaurant_not_found"},
		{`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":2}]}`, domain.ErrItemNotFound, 404, "item_not_found"},
		{`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":2}]}`, application.ErrItemUnavailable, 409, "item_unavailable"},
		{`{"restaurant_id":1,"items":[{"menu_item_id":10,"quantity":2,"price_minor":1}]}`, nil, 400, ""},
	} {
		h := NewInternalHandler(application.NewQuotes(quoteRepository{tt.err}), logger, time.Second)
		request := httptest.NewRequest("POST", "/internal/v1/menu/quote", strings.NewReader(tt.body))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request)
		if w.Code != tt.status {
			t.Fatalf("%s: %d %s", tt.body, w.Code, w.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if tt.code != "" && got["code"] != tt.code {
			t.Fatal(got)
		}
	}
	public := NewHandler(nil, nil, func(context.Context) error { return nil }, logger, time.Second)
	w := httptest.NewRecorder()
	public.ServeHTTP(w, httptest.NewRequest("POST", "/internal/v1/menu/quote", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("public listener exposed quote: %d", w.Code)
	}
}
