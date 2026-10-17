// Package security implements the HTTP identity boundary. Business packages do not
// depend on it; only adapters and bootstrap translate identities to internal IDs.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Principal struct {
	Subject     string   `json:"subject"`
	UserID      int64    `json:"user_id"`
	CustomerID  int64    `json:"customer_id"`
	CourierID   int64    `json:"courier_id"`
	Roles       []string `json:"roles"`
	Restaurants []int64  `json:"restaurants"`
	Permissions []string `json:"permissions"`
}

func (p Principal) Has(role string) bool       { return slices.Contains(p.Roles, role) }
func (p Principal) Can(permission string) bool { return slices.Contains(p.Permissions, permission) }
func (p Principal) Restaurant(id int64) bool   { return id > 0 && slices.Contains(p.Restaurants, id) }

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok
}

type Config struct {
	Environment               string
	DevEnabled                bool
	Issuer, Audience, JWKSURL string
	Identities                []Principal
	DevAdmins                 []int64
	DevRestaurants            map[int64][]int64
}

// Load reads a server-owned identity registry, never claims carrying client-chosen
// roles or numeric IDs. The issuer is part of the registry namespace.
func Load() (Config, error) {
	c := Config{Environment: os.Getenv("APP_ENV"), Issuer: os.Getenv("OIDC_ISSUER"), Audience: os.Getenv("OIDC_AUDIENCE"), JWKSURL: os.Getenv("OIDC_JWKS_URL"), DevRestaurants: map[int64][]int64{}}
	if c.Environment == "" {
		c.Environment = "production"
	}
	var err error
	if raw := os.Getenv("DEV_AUTH_ENABLED"); raw != "" {
		c.DevEnabled, err = strconv.ParseBool(raw)
		if err != nil {
			return c, errors.New("invalid DEV_AUTH_ENABLED")
		}
	}
	if path := os.Getenv("AUTH_IDENTITIES_FILE"); path != "" {
		f, err := os.Open(path)
		if err != nil {
			return c, errors.New("cannot open AUTH_IDENTITIES_FILE")
		}
		defer f.Close()
		var registry struct {
			Issuer     string      `json:"issuer"`
			Identities []Principal `json:"identities"`
		}
		d := json.NewDecoder(io.LimitReader(f, 1<<20))
		d.DisallowUnknownFields()
		if d.Decode(&registry) != nil || d.Decode(new(any)) != io.EOF || registry.Issuer != c.Issuer {
			return c, errors.New("invalid identity registry or issuer namespace")
		}
		c.Identities = registry.Identities
	}
	for _, raw := range strings.Split(os.Getenv("DEV_ADMIN_IDS"), ",") {
		if raw == "" {
			continue
		}
		id, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || id <= 0 {
			return c, errors.New("invalid DEV_ADMIN_IDS")
		}
		c.DevAdmins = append(c.DevAdmins, id)
	}
	for _, raw := range strings.Split(os.Getenv("DEV_RESTAURANT_GRANTS"), ",") {
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) != 2 {
			return c, errors.New("invalid DEV_RESTAURANT_GRANTS")
		}
		user, e1 := strconv.ParseInt(parts[0], 10, 64)
		restaurant, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil || user <= 0 || restaurant <= 0 {
			return c, errors.New("invalid DEV_RESTAURANT_GRANTS")
		}
		c.DevRestaurants[user] = append(c.DevRestaurants[user], restaurant)
	}
	return c, nil
}

type Authenticator struct {
	cfg        Config
	verifier   *oidc.IDTokenVerifier
	identities map[string]Principal
}

func New(c Config) (*Authenticator, error) {
	if c.Environment != "production" && c.Environment != "development" && c.Environment != "test" {
		return nil, errors.New("invalid APP_ENV")
	}
	if c.DevEnabled && c.Environment != "development" && c.Environment != "test" {
		return nil, errors.New("dev authentication forbidden outside development/test")
	}
	c.DevAdmins = slices.Clone(c.DevAdmins)
	grants := make(map[int64][]int64, len(c.DevRestaurants))
	for user, ids := range c.DevRestaurants {
		grants[user] = slices.Clone(ids)
	}
	c.DevRestaurants = grants
	a := &Authenticator{cfg: c, identities: map[string]Principal{}}
	if c.DevEnabled {
		if c.Issuer != "" || c.Audience != "" || c.JWKSURL != "" {
			return nil, errors.New("OIDC and dev authentication are mutually exclusive")
		}
		return a, nil
	}
	// Missing OIDC never enables headers. Bootstrap fails closed even in development.
	if c.Issuer == "" || c.Audience == "" || c.JWKSURL == "" {
		return nil, errors.New("OIDC_ISSUER, OIDC_AUDIENCE and OIDC_JWKS_URL are required")
	}
	for _, raw := range []string{c.Issuer, c.JWKSURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(c.Environment == "test" && u.Scheme == "http")) {
			return nil, errors.New("OIDC endpoints must be trusted HTTPS URLs")
		}
	}
	users := map[int64]bool{}
	customers := map[int64]bool{}
	couriers := map[int64]bool{}
	for _, p := range c.Identities {
		if p.Subject == "" || p.UserID <= 0 || p.CustomerID < 0 || p.CourierID < 0 || users[p.UserID] || a.identities[p.Subject].Subject != "" {
			return nil, errors.New("invalid or ambiguous identity mapping")
		}
		for _, r := range p.Roles {
			if !slices.Contains([]string{"customer", "restaurant_staff", "courier", "dispatcher", "admin"}, r) {
				return nil, errors.New("unknown identity role")
			}
		}
		if (p.Has("customer") && p.CustomerID <= 0) || (p.Has("courier") && p.CourierID <= 0) || (p.CustomerID > 0 && customers[p.CustomerID]) || (p.CourierID > 0 && couriers[p.CourierID]) {
			return nil, errors.New("invalid or ambiguous resource identity mapping")
		}
		for _, id := range p.Restaurants {
			if id <= 0 {
				return nil, errors.New("invalid restaurant grant")
			}
		}
		for _, permission := range p.Permissions {
			if permission != "delivery:dispatch" && permission != "saga:retry" {
				return nil, errors.New("unknown identity permission")
			}
		}
		p.Roles = slices.Clone(p.Roles)
		p.Restaurants = slices.Clone(p.Restaurants)
		p.Permissions = slices.Clone(p.Permissions)
		a.identities[p.Subject] = p
		users[p.UserID] = true
		if p.CustomerID > 0 {
			customers[p.CustomerID] = true
		}
		if p.CourierID > 0 {
			couriers[p.CourierID] = true
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	keyctx := oidc.ClientContext(context.Background(), client)
	a.verifier = oidc.NewVerifier(c.Issuer, oidc.NewRemoteKeySet(keyctx, c.JWKSURL), &oidc.Config{ClientID: c.Audience, SupportedSigningAlgs: []string{oidc.RS256, oidc.ES256}})
	return a, nil
}

// RestaurantGrants/Dispatchers feed the existing application authorization rules.
func (a *Authenticator) RestaurantGrants() map[int64][]int64 {
	out := map[int64][]int64{}
	for _, p := range a.identities {
		if p.Has("restaurant_staff") || p.Has("admin") || p.Has("dispatcher") {
			out[p.UserID] = slices.Clone(p.Restaurants)
		}
	}
	return out
}
func (a *Authenticator) Dispatchers() []int64 {
	var out []int64
	for _, p := range a.identities {
		if (p.Has("dispatcher") || p.Has("admin")) && p.Can("delivery:dispatch") {
			out = append(out, p.UserID)
		}
	}
	return out
}

func deny(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer`)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte("{\"error\":\"access denied\"}\n"))
}
func (a *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if a.cfg.DevEnabled {
			if len(r.Header.Values("Authorization")) > 0 {
				deny(w, 401)
				return
			}
			if values := r.Header.Values("X-Dev-User-ID"); len(values) == 1 {
				id, err := strconv.ParseInt(values[0], 10, 64)
				if err == nil && id > 0 {
					p := Principal{UserID: id, Restaurants: a.cfg.DevRestaurants[id]}
					if slices.Contains(a.cfg.DevAdmins, id) {
						p.Roles = append(p.Roles, "admin")
					}
					if len(p.Restaurants) > 0 {
						p.Roles = append(p.Roles, "restaurant_staff")
					}
					r = r.WithContext(WithPrincipal(r.Context(), p))
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		for key := range r.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-dev-") {
				deny(w, 401)
				return
			}
		}
		values := r.Header.Values("Authorization")
		if len(values) == 0 {
			next.ServeHTTP(w, r)
			return
		} // Public catalog reads and health stay anonymous.
		if len(values) != 1 || len(values[0]) > 16384 {
			deny(w, 401)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			deny(w, 401)
			return
		}
		token, err := a.verifier.Verify(r.Context(), parts[1])
		if err != nil || token.Subject == "" || token.Expiry.IsZero() || token.Issuer != a.cfg.Issuer {
			deny(w, 401)
			return
		}
		// Explicitly reject future nbf and require exp; go-oidc verifies issuer, aud, exp and signature.
		var claims struct {
			NotBefore int64 `json:"nbf"`
			Expires   int64 `json:"exp"`
		}
		if token.Claims(&claims) != nil || claims.Expires <= 0 || claims.NotBefore > time.Now().Unix() {
			deny(w, 401)
			return
		}
		p, ok := a.identities[token.Subject]
		if !ok {
			deny(w, 403)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
