package httpadapter

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCookieSecurityAndCSRFBinding(t *testing.T) {
	h, err := New(nil, Config{Origins: []string{"https://food.example"}, SecureCookies: true, CSRFKey: strings.Repeat("c", 32), ServiceKey: strings.Repeat("s", 32), Timeout: time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	raw := strings.Repeat("r", 43)
	h.setCookie(w, raw, time.Now().Add(time.Hour))
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Domain != "" || cookies[0].Path != "/" || cookies[0].Name != "__Host-food_refresh" {
		t.Fatal("unsafe refresh cookie")
	}
	r := httptest.NewRequest("POST", "/v1/auth/refresh", nil)
	r.AddCookie(cookies[0])
	r.Header.Set("X-CSRF-Token", h.csrf(raw))
	if !h.checkCSRF(r) {
		t.Fatal("own CSRF rejected")
	}
	r.Header.Set("X-CSRF-Token", h.csrf(strings.Repeat("x", 43)))
	if h.checkCSRF(r) {
		t.Fatal("foreign session CSRF accepted")
	}
	r.Header.Set("X-CSRF-Token", h.csrf(raw))
	r.AddCookie(cookies[0])
	if h.checkCSRF(r) {
		t.Fatal("duplicate cookie accepted")
	}
}
func TestUnsafeOriginConfigurationRejected(t *testing.T) {
	for _, origin := range []string{"*", "https://*.example", "https://user@food.example", "http://food.example", "https://food.example/", "null"} {
		_, err := New(nil, Config{Origins: []string{origin}, SecureCookies: true, CSRFKey: strings.Repeat("c", 32), ServiceKey: strings.Repeat("s", 32), Timeout: time.Second}, nil, nil)
		if err == nil {
			t.Errorf("accepted %q", origin)
		}
	}
}
