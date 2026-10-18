package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Desu-php/food/services/orders/internal/application"
)

func TestQuoteHTTP(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"success", 200, `{"restaurant_id":1,"currency":"TJS","items":[{"menu_item_id":10,"restaurant_id":1,"name":"Плов","quantity":2,"unit_price_minor":6500,"currency":"TJS"},{"menu_item_id":11,"restaurant_id":1,"name":"Чай","quantity":1,"unit_price_minor":100,"currency":"TJS"}]}`, nil},
		{"restaurant missing", 404, `{"code":"restaurant_not_found"}`, application.ErrRestaurantNotFound},
		{"item missing", 404, `{"code":"item_not_found"}`, application.ErrItemNotFound},
		{"closed", 409, `{"code":"restaurant_unavailable"}`, application.ErrRestaurantUnavailable},
		{"unavailable", 409, `{"code":"item_unavailable"}`, application.ErrItemUnavailable},
		{"foreign", 409, `{"code":"item_restaurant_mismatch"}`, application.ErrItemRestaurantMismatch},
		{"server failure", 500, `{"error":"private SQL error"}`, application.ErrCatalogUnavailable},
		{"bad JSON", 200, `{`, application.ErrCatalogUnavailable},
		{"incomplete quote", 200, `{"restaurant_id":1,"currency":"TJS","items":[]}`, application.ErrCatalogUnavailable},
		{"redirect", 302, `{}`, application.ErrCatalogUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/internal/v1/menu/quote" {
					t.Error("bad request route")
				}
				var input struct {
					RestaurantID int64                       `json:"restaurant_id"`
					Items        []application.RequestedItem `json:"items"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.RestaurantID != 1 || len(input.Items) != 2 {
					t.Errorf("batch request: %+v %v", input, err)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			items, err := client.Quote(context.Background(), 1, []application.RequestedItem{{MenuItemID: 10, Quantity: 2}, {MenuItemID: 11, Quantity: 1}})
			if !errors.Is(err, tt.want) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if err == nil && (items.Items[0].Name != "Плов" || items.Items[0].UnitPriceMinor != 6500) {
				t.Fatal(items)
			}
		})
	}
}
func TestQuoteTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer server.Close()
	client, _ := NewClient(server.URL, 20*time.Millisecond)
	defer client.Close()
	if _, err := client.Quote(context.Background(), 1, []application.RequestedItem{{MenuItemID: 1, Quantity: 1}}); !errors.Is(err, application.ErrCatalogUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Quote(ctx, 1, nil); !errors.Is(err, application.ErrCatalogUnavailable) {
		t.Fatal(err)
	}
	server.Close()
	if _, err := client.Quote(context.Background(), 1, nil); !errors.Is(err, application.ErrCatalogUnavailable) {
		t.Fatal(err)
	}
}
func TestClientConfiguration(t *testing.T) {
	for _, url := range []string{"", ":bad", "ftp://localhost", "http://user:secret@localhost", "http://localhost/path", "http://localhost?token=secret"} {
		if _, err := NewClient(url, time.Second); err == nil {
			t.Fatalf("accepted %q", url)
		}
	}
}
