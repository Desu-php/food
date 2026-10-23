package tokens

import (
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestArgon2idSaltVerificationAndBounds(t *testing.T) {
	p, err := NewPasswords(19456, 2, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || !strings.HasPrefix(a, "$argon2id$v=19$") || !p.Verify(a, "correct horse battery staple") || p.Verify(a, "wrong password") {
		t.Fatal("Argon2id contract violated")
	}
	for _, hash := range []string{"plain", strings.Replace(a, "m=19456", "m=999999999", 1), strings.Replace(a, "v=19", "v=16", 1)} {
		if p.Verify(hash, "correct horse battery staple") {
			t.Fatal("invalid hash accepted")
		}
	}
	if _, err = NewPasswords(1024, 1, 0, 100); err == nil {
		t.Fatal("unsafe work factor accepted")
	}
}
func TestJWTTrustBoundariesAndKeyRollover(t *testing.T) {
	key, err := GenerateKey("one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := GenerateKey("two")
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://auth.example.test"
	ring := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}}
	j, err := FromKeys(issuer, "food-api", "one", ring, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u := domain.User{Subject: "opaque-subject", Version: 1}
	s := domain.Session{ID: "opaque-session", ExpiresAt: time.Now().Add(time.Hour)}
	raw, _, err := j.Sign(u, s)
	if err != nil {
		t.Fatal(err)
	}
	if c, e := j.Verify(raw); e != nil || c.Subject != u.Subject || c.SessionID != s.ID {
		t.Fatal("own JWT rejected", e)
	}
	publicJSON, _ := j.PublicKeys().Keys[0].MarshalJSON()
	if strings.Contains(string(publicJSON), `"d"`) {
		t.Fatal("private key exposed")
	}
	for _, tc := range []struct {
		name, issuer, aud, typ string
		expiry, nbf            time.Time
		signing                jose.JSONWebKey
	}{
		{"expired", issuer, "food-api", "at+jwt", time.Now().Add(-time.Minute), time.Now().Add(-time.Hour), key},
		{"future", issuer, "food-api", "at+jwt", time.Now().Add(time.Hour), time.Now().Add(time.Hour), key},
		{"audience", issuer, "other-api", "at+jwt", time.Now().Add(time.Hour), time.Now(), key},
		{"issuer", "https://attacker.example", "food-api", "at+jwt", time.Now().Add(time.Hour), time.Now(), key},
		{"signature", issuer, "food-api", "at+jwt", time.Now().Add(time.Hour), time.Now(), other},
		{"id-token", issuer, "food-api", "JWT", time.Now().Add(time.Hour), time.Now(), key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: tc.signing}, new(jose.SignerOptions).WithType(jose.ContentType(tc.typ)).WithHeader("kid", "one"))
			if e != nil {
				t.Fatal(e)
			}
			token, e := jwt.Signed(signer).Claims(map[string]any{"iss": tc.issuer, "aud": tc.aud, "sub": "opaque-subject", "sid": "opaque-session", "av": 1, "exp": tc.expiry.Unix(), "nbf": tc.nbf.Unix(), "iat": time.Now().Unix()}).Serialize()
			if e != nil {
				t.Fatal(e)
			}
			if _, e = j.Verify(token); e == nil {
				t.Fatal("invalid JWT accepted")
			}
		})
	}
	rotated, err := FromKeys(issuer, "food-api", "two", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key.Public(), other}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rotated.Verify(raw); err != nil {
		t.Fatal("retiring key rejected too soon")
	}
	retired, err := FromKeys(issuer, "food-api", "two", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{other}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = retired.Verify(raw); err == nil {
		t.Fatal("removed signing key accepted")
	}
}
