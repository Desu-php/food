package httpadapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/go-jose/go-jose/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeviceOIDCSignedClaimsCannotChooseOwner(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Use: "sig", Algorithm: "RS256"}}})
	}))
	defer jwks.Close()
	auth, err := security.New(security.Config{Environment: "test", Issuer: "https://issuer.example", Audience: "notifications-api", JWKSURL: jwks.URL, Identities: []security.Principal{{Subject: "opaque-customer", UserID: 101, CustomerID: 7, Roles: []string{"customer"}}}})
	if err != nil {
		t.Fatal(err)
	}
	store := &deviceMemory{}
	h := auth.Wrap(DeviceHandler(application.NewDevices(store), NewHandler(func(context.Context) error { return nil }), nil))
	for _, tt := range []struct {
		audience, owner string
		status          int
	}{{"notifications-api", "CUSTOMER", 200}, {"notifications-api", "COURIER", 403}, {"delivery-api", "CUSTOMER", 401}} {
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, new(jose.SignerOptions).WithHeader("kid", "test"))
		if err != nil {
			t.Fatal(err)
		}
		claims, err := json.Marshal(map[string]any{"iss": "https://issuer.example", "aud": tt.audience, "sub": "opaque-customer", "exp": time.Now().Add(time.Minute).Unix(), "roles": []string{"courier", "admin"}, "customer_id": 999, "courier_id": 88})
		if err != nil {
			t.Fatal(err)
		}
		signed, err := signer.Sign(claims)
		if err != nil {
			t.Fatal(err)
		}
		token, err := signed.CompactSerialize()
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/devices", strings.NewReader(`{"owner_type":"`+tt.owner+`","platform":"IOS","installation_id":"private-installation"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tt.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(store.devices) != 1 || store.devices[0].OwnerID != 7 || store.devices[0].RegisteredBy != 101 {
		t.Fatal("JWT client claims escaped server registry")
	}
}
