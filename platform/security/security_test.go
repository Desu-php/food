package security

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

func TestJWTTrustBoundaryAndRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	public := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "first", Algorithm: "RS256", Use: "sig"}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{public}})
	}))
	defer jwks.Close()
	auth, err := New(Config{Environment: "test", Issuer: "https://issuer.example", Audience: "food-api", JWKSURL: jwks.URL, Identities: []Principal{{Subject: "external|opaque-123", UserID: 80, CustomerID: 7, Roles: []string{"customer"}}}})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(k *rsa.PrivateKey, kid string, claims map[string]any) string {
		t.Helper()
		s, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, new(jose.SignerOptions).WithHeader("kid", kid))
		if e != nil {
			t.Fatal(e)
		}
		body, e := json.Marshal(claims)
		if e != nil {
			t.Fatal(e)
		}
		obj, e := s.Sign(body)
		if e != nil {
			t.Fatal(e)
		}
		token, e := obj.CompactSerialize()
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	claims := func() map[string]any {
		return map[string]any{"iss": "https://issuer.example", "aud": "food-api", "sub": "external|opaque-123", "exp": time.Now().Add(time.Hour).Unix(), "roles": []string{"admin"}, "customer_id": 999}
	}
	h := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok {
			w.WriteHeader(401)
			return
		}
		if p.UserID != 80 || p.CustomerID != 7 || p.Has("admin") {
			t.Error("untrusted claims used as identity")
		}
		if expiry, ok := r.Context().Deadline(); !ok || expiry.After(time.Now().Add(time.Hour)) || expiry.Before(time.Now()) {
			t.Error("authenticated request must be bounded by verified token expiry")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		name     string
		change   func(map[string]any)
		signing  *rsa.PrivateKey
		expected int
	}{
		{name: "valid opaque subject", expected: 204},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() }, expected: 401},
		{name: "no expiry", change: func(c map[string]any) { delete(c, "exp") }, expected: 401},
		{name: "wrong issuer", change: func(c map[string]any) { c["iss"] = "https://attacker.example" }, expected: 401},
		{name: "wrong audience", change: func(c map[string]any) { c["aud"] = "another-api" }, expected: 401},
		{name: "future nbf", change: func(c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }, expected: 401},
		{name: "unmapped subject", change: func(c map[string]any) { c["sub"] = "80" }, expected: 403},
		{name: "forged signature", signing: other, expected: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := claims()
			if tc.change != nil {
				tc.change(c)
			}
			k := key
			if tc.signing != nil {
				k = tc.signing
			}
			req := httptest.NewRequest("POST", "/orders", nil)
			req.Header.Set("Authorization", "Bearer "+sign(k, "first", c))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.expected {
				t.Fatalf("got %d want %d", w.Code, tc.expected)
			}
		})
	}
	for _, token := range []string{"x.y.z", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.", ""} {
		req := httptest.NewRequest("POST", "/orders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal("accepted forged token")
		}
	}
	mu.Lock()
	public = jose.JSONWebKey{Key: &other.PublicKey, KeyID: "rotated", Algorithm: "RS256", Use: "sig"}
	mu.Unlock()
	req := httptest.NewRequest("POST", "/orders", nil)
	req.Header.Set("Authorization", "Bearer "+sign(other, "rotated", claims()))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatal("JWKS rotation not recovered", w.Code)
	}
	req = httptest.NewRequest("POST", "/orders", nil)
	req.Header.Set("X-Dev-Customer-ID", "7")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("OIDC accepted dev header")
	}
}

func TestAuthenticationConfigurationFailsClosed(t *testing.T) {
	for _, cfg := range []Config{
		{Environment: "production", DevEnabled: true},
		{Environment: "production"},
		{Environment: "development"},
		{Environment: "test", DevEnabled: true, Issuer: "https://issuer.example"},
		{Environment: "production", Issuer: "https://issuer.example", Audience: "api", JWKSURL: "http://keys.example"},
		{Environment: "production", Issuer: "https://issuer.example", Audience: "api", JWKSURL: "https://keys.example", Identities: []Principal{{Subject: "a", UserID: 1, CustomerID: 7, Roles: []string{"customer"}}, {Subject: "b", UserID: 2, CustomerID: 7, Roles: []string{"customer"}}}},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("unsafe config accepted: %+v", cfg)
		}
	}
	auth, err := New(Config{Environment: "test", DevEnabled: true, DevAdmins: []int64{10}})
	if err != nil {
		t.Fatal(err)
	}
	h := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := FromContext(r.Context())
		if !p.Has("admin") {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	}))
	for _, id := range []string{"10", "20"} {
		r := httptest.NewRequest("POST", "/restaurants", nil)
		r.Header.Set("X-Dev-User-ID", id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if (w.Code == 204) != (id == "10") {
			t.Fatal("dev admin grant bypass")
		}
	}
}

func TestAlgorithmsAreRestricted(t *testing.T) {
	a, err := New(Config{Environment: "test", Issuer: "https://issuer.example", Audience: "api", JWKSURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("a", 32))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := signer.Sign([]byte(`{"iss":"https://issuer.example","aud":"api","sub":"1","exp":9999999999}`))
	token, _ := obj.CompactSerialize()
	if _, err = a.verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("HS256 accepted")
	}
}

func TestProductionRejectsEveryDevIdentityHeader(t *testing.T) {
	auth, err := New(Config{Environment: "production", Issuer: "https://issuer.example", Audience: "api", JWKSURL: "https://issuer.example/keys"})
	if err != nil {
		t.Fatal(err)
	}
	next := auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("dev identity crossed production boundary")
		w.WriteHeader(204)
	}))
	for _, header := range []string{"X-Dev-User-ID", "X-Dev-Customer-ID", "X-Dev-Courier-ID", "X-Dev-Restaurant-ID", "X-Dev-Role"} {
		r := httptest.NewRequest("POST", "/orders", nil)
		r.Header.Set(header, "1")
		w := httptest.NewRecorder()
		next.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(header, w.Code)
		}
	}
}
