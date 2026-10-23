package tokens

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type JWT struct {
	issuer, audience string
	ttl              time.Duration
	signer           jose.Signer
	keys             jose.JSONWebKeySet
	active           string
}
type claims struct {
	jwt.Claims
	SessionID string `json:"sid"`
	Version   int64  `json:"av"`
}

// The key ring contains one active private ES256 key and public retiring keys.
func NewJWT(issuer, audience, active, path string, ttl time.Duration) (*JWT, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read signing key ring")
	}
	var ring jose.JSONWebKeySet
	if json.Unmarshal(body, &ring) != nil {
		return nil, errors.New("invalid signing key ring")
	}
	return FromKeys(issuer, audience, active, ring, ttl)
}
func FromKeys(issuer, audience, active string, ring jose.JSONWebKeySet, ttl time.Duration) (*JWT, error) {
	if issuer == "" || audience == "" || active == "" || ttl < time.Second || ttl > 5*time.Minute {
		return nil, errors.New("invalid JWT configuration")
	}
	j := &JWT{issuer: issuer, audience: audience, ttl: ttl, active: active}
	seen := map[string]bool{}
	for _, key := range ring.Keys {
		if key.KeyID == "" || seen[key.KeyID] || key.Algorithm != "ES256" || key.Use != "sig" || !key.Valid() {
			return nil, errors.New("invalid JWT key")
		}
		seen[key.KeyID] = true
		pub := key.Public()
		p, ok := pub.Key.(*ecdsa.PublicKey)
		if !ok || p.Curve != elliptic.P256() {
			return nil, errors.New("ES256 requires P-256")
		}
		j.keys.Keys = append(j.keys.Keys, pub)
		if key.KeyID == active {
			if _, ok := key.Key.(*ecdsa.PrivateKey); !ok {
				return nil, errors.New("active signing key must be private")
			}
			var err error
			j.signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, new(jose.SignerOptions).WithType("at+jwt").WithHeader("kid", active))
			if err != nil {
				return nil, err
			}
		}
	}
	if j.signer == nil {
		return nil, errors.New("active signing key missing")
	}
	return j, nil
}
func GenerateKey(kid string) (jose.JSONWebKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return jose.JSONWebKey{Key: k, KeyID: kid, Algorithm: "ES256", Use: "sig"}, err
}
func (j *JWT) PublicKeys() jose.JSONWebKeySet { return j.keys }
func (j *JWT) Sign(u domain.User, s domain.Session) (string, time.Time, error) {
	now := time.Now().UTC()
	expires := minTime(now.Add(j.ttl), s.ExpiresAt)
	c := claims{jwt.Claims{Issuer: j.issuer, Subject: u.Subject, Audience: jwt.Audience{j.audience}, Expiry: jwt.NewNumericDate(expires), NotBefore: jwt.NewNumericDate(now), IssuedAt: jwt.NewNumericDate(now), ID: randomID()}, s.ID, u.Version}
	raw, err := jwt.Signed(j.signer).Claims(c).Serialize()
	return raw, expires, err
}
func (j *JWT) Verify(raw string) (domain.Claims, error) {
	if len(raw) > 16384 {
		return domain.Claims{}, domain.ErrCredentials
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(token.Headers) != 1 {
		return domain.Claims{}, domain.ErrCredentials
	}
	typ, _ := token.Headers[0].ExtraHeaders[jose.HeaderType].(string)
	if typ != "at+jwt" {
		return domain.Claims{}, domain.ErrCredentials
	}
	for _, key := range j.keys.Keys {
		if key.KeyID != token.Headers[0].KeyID {
			continue
		}
		var c claims
		if token.Claims(key.Key, &c) != nil || c.Expiry == nil || c.NotBefore == nil || c.IssuedAt == nil || c.Subject == "" || c.SessionID == "" || c.Version < 1 {
			return domain.Claims{}, domain.ErrCredentials
		}
		if c.ValidateWithLeeway(jwt.Expected{Issuer: j.issuer, AnyAudience: jwt.Audience{j.audience}, Time: time.Now()}, 0) != nil {
			return domain.Claims{}, domain.ErrCredentials
		}
		return domain.Claims{Subject: c.Subject, SessionID: c.SessionID, Version: c.Version, ExpiresAt: c.Expiry.Time()}, nil
	}
	return domain.Claims{}, domain.ErrCredentials
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("operating system randomness unavailable")
	}
	return hex.EncodeToString(b)
}
