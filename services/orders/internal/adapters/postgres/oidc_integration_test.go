package postgres

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/orders/internal/adapters/http"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/go-jose/go-jose/v4"
)

func TestOIDCProfileAndFulfillmentAuthorization(t *testing.T) {
	pool := integrationPool(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer jwks.Close()
	auth, err := security.New(security.Config{Environment: "test", Issuer: jwks.URL, Audience: "food-api", JWKSURL: jwks.URL, Identities: []security.Principal{
		{Subject: "oidc-staff", UserID: 10, Roles: []string{"restaurant_staff"}, Restaurants: []int64{1, 2}},
		{Subject: "oidc-customer", UserID: 100, CustomerID: 7, Roles: []string{"customer"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(subject string) string {
		t.Helper()
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, new(jose.SignerOptions).WithHeader("kid", "test"))
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{"iss": jwks.URL, "aud": "food-api", "sub": subject, "exp": time.Now().Add(time.Minute).Unix(), "typ": "Bearer", "roles": []string{"admin"}, "restaurant_id": 999})
		if err != nil {
			t.Fatal(err)
		}
		obj, err := signer.Sign(body)
		if err != nil {
			t.Fatal(err)
		}
		token, err := obj.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	repo := NewOrders(pool)
	delivery, err := repo.Create(context.Background(), "oidc-delivery", strings.Repeat("b", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	pickup := createPickup(t, repo, "oidc-pickup")
	h := auth.CORS(auth.Wrap(httpadapter.NewHandler(application.NewOrders(repo, nil), pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second, false, application.NewRestaurants(repo, auth.RestaurantGrants()))), "/v1/auth/me", "/orders", "/restaurants")
	staff, customer := sign("oidc-staff"), sign("oidc-customer")
	call := func(method, path, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	profile := call("GET", "/v1/auth/me", staff, 200)
	if !strings.Contains(profile.Body.String(), `"restaurant_ids":[1,2]`) {
		t.Fatal(profile.Body)
	}
	call("GET", "/restaurants/1/orders", staff, 200)
	call("GET", "/restaurants/999/orders", staff, 403)
	for _, id := range []int64{delivery.Snapshot().ID, pickup.Snapshot().ID} {
		call("GET", fmt.Sprintf("/orders/%d", id), customer, 200)
		call("POST", fmt.Sprintf("/restaurants/999/orders/%d/accept", id), staff, 403)
		call("POST", fmt.Sprintf("/restaurants/1/orders/%d/accept", id), customer, 403)
		call("POST", fmt.Sprintf("/restaurants/1/orders/%d/accept", id), staff, 200)
	}
	call("POST", fmt.Sprintf("/restaurants/1/orders/%d/prepare", delivery.Snapshot().ID), staff, 409)
	call("POST", fmt.Sprintf("/restaurants/1/orders/%d/prepare", pickup.Snapshot().ID), staff, 200)
	call("POST", fmt.Sprintf("/restaurants/1/orders/%d/ready", pickup.Snapshot().ID), staff, 200)
	// PICKUP still creates no Saga, delivery command or payment confirmation.
	pickupCounts(t, pool, pickup.Snapshot().ID, 0, 0)
	var state string
	// Delivery creation is asynchronous; ACCEPTED waits for its remote result.
	if err := pool.QueryRow(context.Background(), `SELECT status FROM orders WHERE id=$1`, delivery.Snapshot().ID).Scan(&state); err != nil || state != "ACCEPTED" {
		t.Fatal("DELIVERY preparation gate changed", state, err)
	}
}
