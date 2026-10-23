package security

import (
	"net/http"
	"slices"
	"strings"
)

// CORS wraps authentication so browser preflights need no bearer token. Only
// explicitly selected public route families receive cross-origin access.
// Cookies and development identity headers are deliberately not enabled.
func (a *Authenticator) CORS(next http.Handler, publicRoots ...string) http.Handler {
	roots := slices.Clone(publicRoots)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		public := false
		for _, root := range roots {
			if r.URL.Path == root || strings.HasPrefix(r.URL.Path, root+"/") {
				public = true
				break
			}
		}
		if !public {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if len(r.Header.Values("Origin")) != 1 || !slices.Contains(a.cfg.CORSOrigins, origin) {
			deny(w, http.StatusForbidden)
			return
		}
		const methods = "GET, HEAD, POST, PUT, PATCH, DELETE"
		const headers = "Authorization, Content-Type, Idempotency-Key, X-Request-ID"
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if len(r.Header.Values("Access-Control-Request-Method")) != 1 || !slices.Contains(strings.Split(methods, ", "), r.Header.Get("Access-Control-Request-Method")) {
				deny(w, http.StatusForbidden)
				return
			}
			for _, line := range r.Header.Values("Access-Control-Request-Headers") {
				for _, header := range strings.Split(line, ",") {
					if !slices.Contains([]string{"authorization", "content-type", "idempotency-key", "x-request-id"}, strings.ToLower(strings.TrimSpace(header))) {
						deny(w, http.StatusForbidden)
						return
					}
				}
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", methods)
			w.Header().Set("Access-Control-Allow-Headers", headers)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, WWW-Authenticate, Allow, Retry-After")
		next.ServeHTTP(w, r)
	})
}
