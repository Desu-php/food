package httpadapter

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func AuthRevocations(next http.Handler, key string, revoke func(context.Context, int64, time.Time) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/devices/revoke" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" || r.Header.Get("Origin") != "" || len(key) < 32 || len(r.Header.Values("X-Auth-Service-Key")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Auth-Service-Key")), []byte(key)) != 1 {
			http.Error(w, "access denied", 403)
			return
		}
		var in struct {
			UserID   int64     `json:"user_id"`
			NotAfter time.Time `json:"not_after"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.UserID <= 0 || in.NotAfter.IsZero() {
			http.Error(w, "invalid request", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if revoke(ctx, in.UserID, in.NotAfter) != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
}
