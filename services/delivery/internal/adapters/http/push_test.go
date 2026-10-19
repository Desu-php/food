package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type offerLookup struct{}

func (offerLookup) OfferLive(_ context.Context, id, courier int64) (bool, error) {
	return id == 1 && courier == 7, nil
}
func TestPushRelevanceRequiresServiceCredential(t *testing.T) {
	key := "test-only-relevance-credential-32-bytes"
	h := PushRelevance(http.NotFoundHandler(), offerLookup{}, key)
	for _, tt := range []struct {
		auth, path string
		code       int
		body       string
	}{{"", "/internal/push/offers/1?courier_id=7", 401, ""}, {"Bearer user-token", "/internal/push/offers/1?courier_id=7", 401, ""}, {"Bearer " + key, "/internal/push/offers/1?courier_id=7", 200, `"live":true`}, {"Bearer " + key, "/internal/push/offers/1?courier_id=8", 200, `"live":false`}, {"Bearer " + key, "/internal/push/offers/bad?courier_id=7", 400, ""}} {
		r := httptest.NewRequest(http.MethodGet, tt.path, nil)
		r.Header.Set("Authorization", tt.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.body) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
