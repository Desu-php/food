package httpadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/go-jose/go-jose/v4"
)

func TestProfilePublicDTO(t *testing.T) {
	h := NewHandler(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	for _, p := range []security.Principal{
		{Subject: "private-subject", UserID: 10, CustomerID: 50, CourierID: 60, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1, 2}},
		{Subject: "no-grants", UserID: 20},
		{Subject: "operator", UserID: 30, Roles: []string{"dispatcher"}, Restaurants: []int64{2}, Permissions: []string{"delivery:dispatch", "saga:retry"}},
	} {
		r := httptest.NewRequest("GET", "/v1/auth/me?user_id=999&restaurant_id=999", strings.NewReader(`{"user_id":999,"roles":["admin"]}`))
		r = r.WithContext(security.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := profileDTO{p.UserID, append([]string{}, p.Roles...), append([]int64{}, p.Restaurants...), append([]string{}, p.Permissions...)}
		var got profileDTO
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || !reflect.DeepEqual(got, want) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body, got)
		}
		var fields map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 4 || strings.Contains(w.Body.String(), "null") {
			t.Fatal("profile schema leaked fields or null arrays", w.Body)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/v1/auth/me", nil))
		if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
			t.Fatal(method, w.Code)
		}
	}
}

func TestProfileOIDCIntegration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys"})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Use: "sig", Algorithm: "RS256"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	issuer = provider.URL
	auth, err := security.New(security.Config{Environment: "test", Issuer: issuer, Audience: "food-api", JWKSURL: issuer + "/keys", CORSOrigins: []string{"https://spa.example"}, Identities: []security.Principal{{Subject: "oidc-staff-secret", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1, 2}}}})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := auth.CORS(auth.Wrap(NewHandler(nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)), time.Second, false, application.NewRestaurants(nil, auth.RestaurantGrants()))), "/v1/auth/me", "/restaurants")
	sign := func(claims map[string]any, k *rsa.PrivateKey, typ string) string {
		t.Helper()
		opts := new(jose.SignerOptions).WithHeader("kid", "test")
		if typ != "" {
			opts.WithType(jose.ContentType(typ))
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, opts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		obj, err := signer.Sign(b)
		if err != nil {
			t.Fatal(err)
		}
		token, err := obj.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	for _, tc := range []struct {
		name                   string
		change                 func(map[string]any)
		otherKey, missing, dev bool
		headerType             string
		status                 int
	}{
		{name: "valid signed Bearer access token", status: 200},
		{name: "missing token", missing: true, status: 401},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, status: 401},
		{name: "invalid signature", otherKey: true, status: 401},
		{name: "invalid issuer", change: func(c map[string]any) { c["iss"] = "https://evil.example" }, status: 401},
		{name: "invalid audience", change: func(c map[string]any) { c["aud"] = "food-spa" }, status: 401},
		{name: "unregistered subject", change: func(c map[string]any) { c["sub"] = "10" }, status: 403},
		{name: "ID token with API audience", change: func(c map[string]any) { c["typ"] = "ID" }, status: 401},
		{name: "OIDC ID token without type", change: func(c map[string]any) { delete(c, "typ"); c["nonce"] = "nonce" }, headerType: "JWT", status: 401},
		{name: "RFC access token", change: func(c map[string]any) { delete(c, "typ") }, headerType: "at+jwt", status: 200},
		{name: "RFC full media type", change: func(c map[string]any) { delete(c, "typ") }, headerType: "application/at+jwt", status: 200},
		{name: "dev headers forbidden in OIDC", dev: true, status: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": "food-api", "sub": "oidc-staff-secret", "exp": time.Now().Add(time.Minute).Unix(), "typ": "Bearer", "roles": []string{"admin"}, "restaurant_ids": []int64{999}, "user_id": 999, "email": "private@example.com"}
			if tc.change != nil {
				tc.change(claims)
			}
			k := key
			if tc.otherKey {
				k = other
			}
			token := sign(claims, k, tc.headerType)
			r := httptest.NewRequest("GET", "/v1/auth/me?user_id=999", nil)
			r.Header.Set("Origin", "https://spa.example")
			if !tc.missing {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			if tc.dev {
				r.Header.Set("X-Dev-User-ID", "10")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "https://spa.example" {
				t.Fatal(w.Code, w.Header(), w.Body)
			}
			if tc.status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing bearer challenge")
			}
			if tc.status == 200 && w.Body.String() != "{\"user_id\":10,\"roles\":[\"restaurant_staff\"],\"restaurant_ids\":[1,2],\"permissions\":[]}\n" {
				t.Fatal(w.Body)
			}
			for _, secret := range []string{token, "oidc-staff-secret", "private@example.com", "access_token", "refresh_token", "jwks", "OIDC_"} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(logs.String(), secret) {
					t.Fatal("sensitive field escaped profile boundary")
				}
			}
			if tc.status == 200 {
				r = httptest.NewRequest("POST", "/restaurants/999/orders/1/accept", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 403 {
					t.Fatal("foreign restaurant authorized", w.Code)
				}
			}
		})
	}
	// Unauthenticated CORS preflight must finish before JWT validation.
	r := httptest.NewRequest("OPTIONS", "/v1/auth/me", nil)
	r.Header.Set("Origin", "https://spa.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", "Authorization")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestProfileDoesNotReadIdentityHeaders(t *testing.T) {
	h := NewHandler(nil, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	r := httptest.NewRequest("GET", "/v1/auth/me?user_id=10&access_token=secret", nil)
	r.Header.Set("X-Dev-User-ID", "10")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
}
