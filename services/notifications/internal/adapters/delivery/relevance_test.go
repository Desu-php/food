package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRelevanceFailsClosed(t *testing.T) {
	for _, status := range []int{200, 401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-service-credential" || r.URL.Path != "/internal/push/offers/1" || r.URL.Query().Get("courier_id") != "7" {
					t.Error("request binding incorrect")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"live":false}`))
			}))
			defer server.Close()
			r := &Relevance{BaseURL: server.URL, Key: "private-service-credential", Client: &http.Client{Timeout: time.Second}}
			live, err := r.Live(context.Background(), 1, 7)
			if live || (status == 200) != (err == nil) {
				t.Fatal(live, err)
			}
		})
	}
}
