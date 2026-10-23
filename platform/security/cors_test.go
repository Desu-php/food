package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSOriginsFailClosed(t *testing.T) {
	for _, origin := range []string{"*", "null", "https://*.example", "https://spa.example/", "https://user@spa.example", "https://spa.example?x", "https://spa.example#x", "", "http://spa.example", "https://spa.example\n"} {
		_, err := New(Config{Environment: "production", Issuer: "https://issuer.example", Audience: "api", JWKSURL: "https://issuer.example/keys", CORSOrigins: []string{origin}})
		if err == nil {
			t.Errorf("accepted origin %q", origin)
		}
	}
}

func TestCORSPreflightAndAuthenticationBoundary(t *testing.T) {
	a, err := New(Config{Environment: "test", DevEnabled: true, CORSOrigins: []string{"https://spa.example", "http://localhost:9000"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := a.CORS(a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		deny(w, 401)
	})), "/v1/auth/me", "/restaurants")
	for _, tc := range []struct {
		name, method, path, origin, requestedMethod, headers string
		status, calls                                        int
		allow                                                bool
	}{
		{"profile preflight", "OPTIONS", "/v1/auth/me", "https://spa.example", "GET", "authorization", 204, 0, true},
		{"pricing PUT", "OPTIONS", "/restaurants/1/delivery-settings", "https://spa.example", "PUT", "Authorization, Content-Type, Idempotency-Key, X-Request-ID", 204, 0, true},
		{"allowed local origin", "OPTIONS", "/v1/auth/me", "http://localhost:9000", "GET", "authorization", 204, 0, true},
		{"untrusted origin", "OPTIONS", "/v1/auth/me", "https://evil.example", "GET", "authorization", 403, 0, false},
		{"opaque origin", "GET", "/v1/auth/me", "null", "", "", 403, 0, false},
		{"actual untrusted origin", "POST", "/restaurants", "https://spa.example.evil", "", "", 403, 0, false},
		{"unlisted method", "OPTIONS", "/v1/auth/me", "https://spa.example", "TRACE", "authorization", 403, 0, false},
		{"dev header", "OPTIONS", "/v1/auth/me", "https://spa.example", "GET", "X-Dev-User-ID", 403, 0, false},
		{"service secret header", "OPTIONS", "/restaurants", "https://spa.example", "GET", "X-Service-Key", 403, 0, false},
		{"auth error visible to SPA", "GET", "/v1/auth/me", "https://spa.example", "", "", 401, 1, true},
		{"native request", "GET", "/v1/auth/me", "", "", "", 401, 1, false},
		{"health excluded", "OPTIONS", "/health", "https://spa.example", "GET", "authorization", 401, 1, false},
		{"internal excluded", "OPTIONS", "/internal/quote", "https://spa.example", "POST", "authorization", 401, 1, false},
		{"metrics excluded", "OPTIONS", "/metrics", "https://spa.example", "GET", "authorization", 401, 1, false},
		{"prefix boundary", "OPTIONS", "/restaurants-private", "https://spa.example", "GET", "authorization", 401, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.requestedMethod != "" {
				r.Header.Set("Access-Control-Request-Method", tc.requestedMethod)
			}
			if tc.headers != "" {
				r.Header.Set("Access-Control-Request-Headers", tc.headers)
			}
			before := calls
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || calls-before != tc.calls {
				t.Fatalf("status=%d calls=%d", w.Code, calls-before)
			}
			allowed := w.Header().Get("Access-Control-Allow-Origin")
			if (allowed != "") != tc.allow || (tc.allow && allowed != tc.origin) || w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("incorrect CORS boundary", w.Header())
			}
			if tc.allow && (!strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Origin") || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("missing cache protection")
			}
		})
	}
}
