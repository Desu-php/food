package httpadapter

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/auth/internal/application"
	"github.com/Desu-php/food/services/auth/internal/domain"
)

type Config struct {
	Origins             []string
	SecureCookies       bool
	CSRFKey, ServiceKey string
	Timeout             time.Duration
}
type Handler struct {
	auth   *application.Auth
	cfg    Config
	logger *slog.Logger
	ready  func(context.Context) error
}
type profile struct {
	UserID          int64      `json:"user_id"`
	Email           string     `json:"email"`
	Status          string     `json:"status"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	Roles           []string   `json:"roles"`
	RestaurantIDs   []int64    `json:"restaurant_ids"`
	Permissions     []string   `json:"permissions"`
}

func dto(u domain.User) profile {
	return profile{u.ID, u.Email, u.Status, u.EmailVerifiedAt, append([]string{}, u.Roles...), append([]int64{}, u.Restaurants...), append([]string{}, u.Permissions...)}
}
func New(auth *application.Auth, cfg Config, logger *slog.Logger, ready func(context.Context) error) (*Handler, error) {
	if len(cfg.CSRFKey) < 32 || len(cfg.ServiceKey) < 32 || cfg.Timeout <= 0 || len(cfg.Origins) == 0 {
		return nil, errors.New("incomplete auth HTTP security configuration")
	}
	for _, o := range cfg.Origins {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || strings.ContainsAny(o, "*\r\n\t ") || (u.Scheme != "https" && !(u.Scheme == "http" && !cfg.SecureCookies && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return nil, errors.New("auth origins require exact trusted origins")
		}
	}
	cfg.Origins = slices.Clone(cfg.Origins)
	return &Handler{auth, cfg, logger, ready}, nil
}
func (h *Handler) cookieName() string {
	if h.cfg.SecureCookies {
		return "__Host-food_refresh"
	}
	return "food_refresh_dev"
}
func (h *Handler) csrf(raw string) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.CSRFKey))
	mac.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (h *Handler) setCookie(w http.ResponseWriter, raw string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if raw == "" {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: h.cookieName(), Value: raw, Path: "/", HttpOnly: true, Secure: h.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}
func (h *Handler) refreshCookie(r *http.Request) string {
	cookies := r.CookiesNamed(h.cookieName())
	if len(cookies) != 1 || len(cookies[0].Value) != 43 {
		return ""
	}
	return cookies[0].Value
}
func (h *Handler) checkCSRF(r *http.Request) bool {
	raw := h.refreshCookie(r)
	values := r.Header.Values("X-CSRF-Token")
	return raw != "" && len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(h.csrf(raw))) == 1
}
func (h *Handler) Public(keys any) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if h.ready(r.Context()) != nil {
			write(w, 503, map[string]string{"error": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		write(w, 200, keys)
	})
	m.HandleFunc("POST /v1/auth/register", h.register)
	m.HandleFunc("POST /v1/auth/login", h.login)
	m.HandleFunc("POST /v1/auth/refresh", h.refresh)
	m.HandleFunc("POST /v1/auth/logout", h.logout)
	m.HandleFunc("POST /v1/auth/logout-all", h.logoutAll)
	m.HandleFunc("GET /v1/auth/csrf", h.csrfToken)
	m.HandleFunc("GET /v1/auth/me", h.me)
	m.HandleFunc("POST /v1/auth/password", h.changePassword)
	m.HandleFunc("GET /v1/auth/sessions", h.sessions)
	m.HandleFunc("DELETE /v1/auth/sessions/{id}", h.revokeSession)
	m.HandleFunc("PUT /v1/auth/admin/users/{id}/bindings", h.bindings)
	for _, path := range []string{"/v1/auth/password-recovery", "/v1/auth/verify-email"} {
		m.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			write(w, 503, map[string]string{"error": "email_channel_not_configured"})
		})
	}
	for path, allow := range map[string]string{"/v1/auth/register": "POST", "/v1/auth/login": "POST", "/v1/auth/refresh": "POST", "/v1/auth/logout": "POST", "/v1/auth/logout-all": "POST", "/v1/auth/csrf": "GET, HEAD", "/v1/auth/me": "GET, HEAD", "/v1/auth/password": "POST", "/v1/auth/sessions": "GET, HEAD", "/v1/auth/sessions/{id}": "DELETE", "/v1/auth/admin/users/{id}/bindings": "PUT", "/v1/auth/password-recovery": "POST", "/v1/auth/verify-email": "POST"} {
		m.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			write(w, 405, map[string]string{"error": "method_not_allowed"})
		})
	}
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { write(w, 404, map[string]string{"error": "not_found"}) })
	return h.boundary(m)
}
func (h *Handler) boundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()
		r = r.WithContext(ctx)
		defer func() {
			if recover() != nil {
				h.logger.Error("auth request panic")
				write(w, 500, map[string]string{"error": "internal_error"})
			}
		}()
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			for key := range r.Header {
				if strings.HasPrefix(strings.ToLower(key), "x-dev-") {
					write(w, 401, map[string]string{"error": "invalid_credentials"})
					return
				}
			}
			w.Header().Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			trusted := len(r.Header.Values("Origin")) == 1 && slices.Contains(h.cfg.Origins, origin)
			if origin != "" && !trusted || r.Method != http.MethodGet && r.Method != http.MethodHead && !trusted || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				write(w, 403, map[string]string{"error": "origin_denied"})
				return
			}
			if trusted {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
			}
			if r.Method == http.MethodOptions {
				if !trusted || !slices.Contains([]string{"GET", "POST", "PUT", "DELETE"}, r.Header.Get("Access-Control-Request-Method")) {
					write(w, 403, map[string]string{"error": "cors_denied"})
					return
				}
				for _, v := range r.Header.Values("Access-Control-Request-Headers") {
					for _, name := range strings.Split(v, ",") {
						if !slices.Contains([]string{"authorization", "content-type", "x-csrf-token"}, strings.ToLower(strings.TrimSpace(name))) {
							write(w, 403, map[string]string{"error": "cors_denied"})
							return
						}
					}
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token")
				w.Header().Add("Vary", "Access-Control-Request-Method")
				w.Header().Add("Vary", "Access-Control-Request-Headers")
				w.WriteHeader(204)
				return
			}
			if err := h.auth.Limit(ctx, "http:"+peer(r), 600, 5*time.Minute); err != nil {
				h.fail(w, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func peer(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		write(w, 415, map[string]string{"error": "json_required"})
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		write(w, 400, map[string]string{"error": "invalid_request"})
		return false
	}
	return true
}
func (h *Handler) fail(w http.ResponseWriter, err error) {
	status, code := 500, "internal_error"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(err, domain.ErrCredentials):
		status, code = 401, "invalid_credentials"
		w.Header().Set("WWW-Authenticate", "Bearer")
	case errors.Is(err, domain.ErrForbidden):
		status, code = 403, "access_denied"
	case errors.Is(err, domain.ErrLimited):
		status, code = 429, "try_later"
		w.Header().Set("Retry-After", "900")
	case errors.Is(err, domain.ErrUnavailable):
		status, code = 503, "unavailable"
	case errors.Is(err, domain.ErrConflict):
		status, code = 409, "identity_conflict"
	default:
		h.logger.Error("auth operation failed", "error_type", "storage_or_signing")
	}
	write(w, status, map[string]string{"error": code})
}
func bearer(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 16384 {
		return ""
	}
	p := strings.Fields(values[0])
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		return ""
	}
	return p[1]
}
func (h *Handler) identity(w http.ResponseWriter, r *http.Request) (domain.User, domain.Claims, bool) {
	u, c, err := h.auth.Authenticate(r.Context(), bearer(r))
	if err != nil {
		h.fail(w, err)
		return u, c, false
	}
	return u, c, true
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.auth.Register(r.Context(), in.Email, in.Password, peer(r)); err != nil {
		h.fail(w, err)
		return
	}
	write(w, 202, map[string]string{"status": "accepted"})
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := h.auth.Login(r.Context(), in.Email, in.Password, peer(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	h.result(w, result)
}
func (h *Handler) result(w http.ResponseWriter, result application.Result) {
	h.setCookie(w, result.RefreshToken, result.Session.ExpiresAt)
	write(w, 200, struct {
		AccessToken string    `json:"access_token"`
		TokenType   string    `json:"token_type"`
		ExpiresIn   int       `json:"expires_in"`
		ExpiresAt   time.Time `json:"expires_at"`
		CSRFToken   string    `json:"csrf_token"`
		User        profile   `json:"user"`
	}{result.AccessToken, "Bearer", max(0, int(time.Until(result.ExpiresAt).Seconds())), result.ExpiresAt, h.csrf(result.RefreshToken), dto(result.User)})
}
func (h *Handler) csrfToken(w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(h.cfg.Origins, r.Header.Get("Origin")) {
		h.fail(w, domain.ErrForbidden)
		return
	}
	raw := h.refreshCookie(r)
	if raw == "" {
		h.fail(w, domain.ErrCredentials)
		return
	}
	write(w, 200, map[string]string{"csrf_token": h.csrf(raw)})
}
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	if !h.checkCSRF(r) {
		h.fail(w, domain.ErrForbidden)
		return
	}
	result, err := h.auth.Refresh(r.Context(), h.refreshCookie(r), peer(r))
	if err != nil {
		if errors.Is(err, domain.ErrCredentials) {
			h.setCookie(w, "", time.Time{})
		}
		h.fail(w, err)
		return
	}
	h.result(w, result)
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	if !h.checkCSRF(r) {
		h.fail(w, domain.ErrForbidden)
		return
	}
	if err := h.auth.LogoutToken(r.Context(), h.refreshCookie(r)); err != nil {
		h.fail(w, err)
		return
	}
	h.setCookie(w, "", time.Time{})
	write(w, 204, nil)
}
func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	_, c, ok := h.identity(w, r)
	if !ok {
		return
	}
	if err := h.auth.Logout(r.Context(), c, true); err != nil {
		h.fail(w, err)
		return
	}
	h.setCookie(w, "", time.Time{})
	write(w, 204, nil)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, _, ok := h.identity(w, r)
	if ok {
		write(w, 200, dto(u))
	}
}
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.auth.ChangePassword(r.Context(), c, in.Current, in.Next); err != nil {
		h.fail(w, err)
		return
	}
	h.setCookie(w, "", time.Time{})
	write(w, 204, nil)
}
func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.identity(w, r)
	if !ok {
		return
	}
	sessions, err := h.auth.Sessions(r.Context(), c)
	if err != nil {
		h.fail(w, err)
		return
	}
	write(w, 200, sessions)
}
func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.identity(w, r)
	if !ok {
		return
	}
	if err := h.auth.RevokeSession(r.Context(), c, r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	write(w, 204, nil)
}
func (h *Handler) bindings(w http.ResponseWriter, r *http.Request) {
	_, c, ok := h.identity(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.fail(w, domain.ErrInvalid)
		return
	}
	var in struct {
		Roles       []string `json:"roles"`
		Permissions []string `json:"permissions"`
		Restaurants []int64  `json:"restaurant_ids"`
		CustomerID  int64    `json:"customer_id"`
		CourierID   int64    `json:"courier_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err = h.auth.UpdateBindings(r.Context(), c, id, domain.User{Roles: in.Roles, Permissions: in.Permissions, Restaurants: in.Restaurants, CustomerID: in.CustomerID, CourierID: in.CourierID}); err != nil {
		h.fail(w, err)
		return
	}
	write(w, 204, nil)
}
func principal(u domain.User) security.Principal {
	return security.Principal{Subject: u.Subject, UserID: u.ID, CustomerID: u.CustomerID, CourierID: u.CourierID, Roles: append([]string{}, u.Roles...), Restaurants: append([]int64{}, u.Restaurants...), Permissions: append([]string{}, u.Permissions...)}
}
func (h *Handler) Internal() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /internal/v1/principal", func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := h.identity(w, r)
		if ok {
			write(w, 200, principal(u))
		}
	})
	m.HandleFunc("POST /internal/v1/device-authorized", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			UserID    int64  `json:"user_id"`
			OwnerType string `json:"owner_type"`
			OwnerID   int64  `json:"owner_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		allowed, err := h.auth.DeviceAuthorized(r.Context(), in.UserID, in.OwnerType, in.OwnerID)
		if err != nil {
			h.fail(w, err)
			return
		}
		write(w, 200, map[string]bool{"allowed": allowed})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		keys := r.Header.Values("X-Auth-Service-Key")
		if r.Header.Get("Origin") != "" || len(keys) != 1 || subtle.ConstantTimeCompare([]byte(keys[0]), []byte(h.cfg.ServiceKey)) != 1 {
			write(w, 403, map[string]string{"error": "access_denied"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()
		m.ServeHTTP(w, r.WithContext(ctx))
	})
}
