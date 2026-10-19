package httpadapter

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

type OfferLookup interface {
	OfferLive(context.Context, int64, int64) (bool, error)
}

// PushRelevance uses a deployment-owned service credential; end-user claims never
// grant access to this private operation. Deploy behind the internal network.
func PushRelevance(next http.Handler, repo OfferLookup, key string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", next)
	mux.HandleFunc("GET /internal/push/offers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(key) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			http.Error(w, "access denied", 401)
			return
		}
		id, e1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		courier, e2 := strconv.ParseInt(r.URL.Query().Get("courier_id"), 10, 64)
		if e1 != nil || e2 != nil || id <= 0 || courier <= 0 {
			http.Error(w, "invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		live, err := repo.OfferLive(ctx, id, courier)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"live": live})
	})
	return mux
}
