package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/auth/internal/adapters/http"
	"github.com/Desu-php/food/services/auth/internal/adapters/tokens"
	"github.com/Desu-php/food/services/auth/internal/application"
	"github.com/Desu-php/food/services/auth/internal/domain"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	repo      *Repository
	svc       *application.Auth
	admin     *application.Administration
	passwords *tokens.Passwords
	jwt       *tokens.JWT
}

func setup(t *testing.T, unverified bool) *fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sql, err := os.ReadFile("../../../migrations/000001_auth.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	repo := New(pool)
	p, err := tokens.NewPasswords(19456, 2, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	key, err := tokens.GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	j, err := tokens.FromKeys("https://auth.example.test", "food-api", "test", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s, err := application.New(repo, p, j, application.Config{SessionTTL: time.Hour, AllowUnverified: unverified})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{repo, s, application.NewAdministration(repo, p), p, j}
}

const password = "a correct and long passphrase"

func (f *fixture) register(t *testing.T, email string) {
	t.Helper()
	if err := f.svc.Register(context.Background(), email, password, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) login(t *testing.T, email string) application.Result {
	t.Helper()
	r, err := f.svc.Login(context.Background(), email, password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *fixture) authenticate(t *testing.T, r application.Result) domain.Claims {
	t.Helper()
	_, c, err := f.svc.Authenticate(context.Background(), r.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegistrationVerificationAndBruteForce(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()
	f.register(t, "Customer@Example.test")
	f.register(t, "customer@example.test")
	var count int
	if err := f.repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_users`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	u, err := f.repo.ByEmail(ctx, "customer@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if u.Status != domain.PendingVerification || u.EmailVerifiedAt != nil || !f.passwords.Verify(u.PasswordHash, password) || u.PasswordHash == password || len(u.Roles) != 1 || u.Roles[0] != "customer" || u.CustomerID < 1000000000000 {
		t.Fatal("invalid registration state")
	}
	if _, err = f.svc.Login(ctx, u.Email, password, "pending"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("unverified public signup allowed", err)
	}
	if _, err = f.repo.pool.Exec(ctx, `UPDATE auth_users SET status='ACTIVE' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, err = f.svc.Login(ctx, "unknown@example.test", "wrong", "unknown-peer")
		if !errors.Is(err, domain.ErrCredentials) {
			t.Fatal(err)
		}
	}
	if _, err = f.svc.Login(ctx, "unknown@example.test", "wrong", "unknown-peer"); !errors.Is(err, domain.ErrLimited) {
		t.Fatal("unknown users must be rate limited identically", err)
	}
	for i := 0; i < 9; i++ {
		_, err = f.svc.Login(ctx, u.Email, "wrong", "known-peer")
		if !errors.Is(err, domain.ErrCredentials) {
			t.Fatal(err)
		}
	}
	if _, err = f.svc.Login(ctx, u.Email, password, "known-peer"); !errors.Is(err, domain.ErrLimited) {
		t.Fatal("brute force bypass", err)
	}
}
func TestRefreshReuseLogoutPasswordAndSessionOwnership(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	f.register(t, "one@example.test")
	f.register(t, "two@example.test")
	first := f.login(t, "one@example.test")
	f.authenticate(t, first)
	next, err := f.svc.Refresh(ctx, first.RefreshToken, "peer")
	if err != nil || next.RefreshToken == first.RefreshToken {
		t.Fatal("rotation", err)
	}
	if _, err = f.svc.Refresh(ctx, first.RefreshToken, "peer"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("old refresh accepted", err)
	}
	if _, _, err = f.svc.Authenticate(ctx, next.AccessToken); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("reuse did not revoke family", err)
	}
	if _, err = f.svc.Refresh(ctx, next.RefreshToken, "peer"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("revoked successor accepted")
	}
	a := f.login(t, "one@example.test")
	b := f.login(t, "one@example.test")
	other := f.login(t, "two@example.test")
	ca := f.authenticate(t, a)
	if err = f.svc.RevokeSession(ctx, ca, other.Session.ID); err != nil {
		t.Fatal(err)
	}
	f.authenticate(t, other)
	sessions, err := f.svc.Sessions(ctx, ca)
	if err != nil || len(sessions) != 2 {
		t.Fatal(len(sessions), err)
	}
	if err = f.svc.LogoutToken(ctx, a.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.LogoutToken(ctx, a.RefreshToken); err != nil {
		t.Fatal("logout retry", err)
	}
	if _, _, err = f.svc.Authenticate(ctx, a.AccessToken); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("logout did not revoke access")
	}
	cb := f.authenticate(t, b)
	if err = f.svc.Logout(ctx, cb, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Refresh(ctx, b.RefreshToken, "peer"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("logout-all did not revoke refresh")
	}
	f.authenticate(t, other)
	c := f.login(t, "one@example.test")
	d := f.login(t, "one@example.test")
	cc := f.authenticate(t, c)
	if err = f.svc.ChangePassword(ctx, cc, "wrong", "a different long passphrase"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal(err)
	}
	if err = f.svc.ChangePassword(ctx, cc, password, "a different long passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.svc.Authenticate(ctx, d.AccessToken); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("password change retained session")
	}
	if _, err = f.svc.Login(ctx, "one@example.test", password, "peer"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("old password accepted")
	}
	if _, err = f.svc.Login(ctx, "one@example.test", "a different long passphrase", "peer"); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentRefreshCommitsOneSuccessAndRevokesFamily(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	f.register(t, "concurrent@example.test")
	initial := f.login(t, "concurrent@example.test")
	var successes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Refresh(ctx, initial.RefreshToken, "concurrent-peer")
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, domain.ErrCredentials) {
				t.Errorf("unexpected refresh error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("rotation was not atomic", successes.Load())
	}
	var n int
	if err := f.repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_refresh_tokens`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	restarted := New(f.repo.pool)
	c, err := f.jwt.Verify(initial.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Authenticate(ctx, c); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("revocation lost after restart", err)
	}
}
func TestMigrationPreservesIDsAndPermissionsAreRevocable(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	legacy := []domain.User{{ID: 10, Subject: "legacy-staff", Roles: []string{"restaurant_staff"}, Restaurants: []int64{1, 2}}, {ID: 101, Subject: "legacy-customer", CustomerID: 7, Roles: []string{"customer"}}, {ID: 103, Subject: "legacy-courier", CourierID: 1, Roles: []string{"courier"}}}
	if err := f.admin.Import(ctx, "https://old.example/realms/food", legacy, "migration-test"); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.Import(ctx, "https://old.example/realms/food", legacy, "migration-test"); err != nil {
		t.Fatal("import retry", err)
	}
	var status string
	var cid int64
	if err := f.repo.pool.QueryRow(ctx, `SELECT status,customer_id FROM auth_users WHERE id=101`).Scan(&status, &cid); err != nil || status != domain.Disabled || cid != 7 {
		t.Fatal(status, cid, err)
	}
	staff := legacy[0]
	staff.Email = "staff@example.test"
	if err := f.admin.Provision(ctx, staff, password, "identity-check-admin"); err != nil {
		t.Fatal(err)
	}
	if err := f.admin.Provision(ctx, domain.User{Email: "admin@example.test", Roles: []string{"admin"}}, password, "bootstrap-admin"); err != nil {
		t.Fatal(err)
	}
	admin := f.login(t, "admin@example.test")
	actor := f.authenticate(t, admin)
	s := f.login(t, staff.Email)
	sc := f.authenticate(t, s)
	if s.User.ID != 10 || s.User.Subject != "legacy-staff" || s.User.EmailVerifiedAt != nil {
		t.Fatal("identity or verification invented")
	}
	if err := f.svc.UpdateBindings(ctx, sc, 10, domain.User{Roles: []string{"admin"}}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("self escalation allowed", err)
	}
	if err := f.svc.UpdateBindings(ctx, actor, 10, domain.User{Roles: []string{"restaurant_staff", "courier"}, Restaurants: []int64{1}, CourierID: 1}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("foreign courier binding was not a conflict", err)
	}
	f.authenticate(t, s)
	allowed, err := f.svc.DeviceAuthorized(ctx, 10, "RESTAURANT", 1)
	if err != nil || !allowed {
		t.Fatal(err)
	}
	if err = f.svc.UpdateBindings(ctx, actor, 10, domain.User{Roles: []string{"restaurant_staff"}, Restaurants: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.svc.Authenticate(ctx, s.AccessToken); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("old permissions still valid")
	}
	allowed, err = f.svc.DeviceAuthorized(ctx, 10, "RESTAURANT", 1)
	if err != nil || allowed {
		t.Fatal("revoked grant still authorizes push", err)
	}
	newSession := f.login(t, staff.Email)
	if len(newSession.User.Restaurants) != 1 || newSession.User.Restaurants[0] != 2 {
		t.Fatal("new permission state missing")
	}
	var queued int
	if err = f.repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_device_revocations WHERE user_id=10`).Scan(&queued); err != nil || queued != 1 {
		t.Fatal("device revocation not atomic", err)
	}
	if err = f.admin.Import(ctx, "https://other.example", []domain.User{{ID: 10, Subject: "collision", Roles: []string{"admin"}}}, "migration-test"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("conflicting ID import accepted", err)
	}
	f.register(t, "fresh@example.test")
	fresh, err := f.repo.ByEmail(ctx, "fresh@example.test")
	if err != nil || fresh.ID <= 103 || fresh.CustomerID == 7 {
		t.Fatal("allocator reused historical identity", err)
	}
}
func TestHTTPAuthenticationCSRFAndLiveAuthority(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	h, err := httpadapter.New(f.svc, httpadapter.Config{Origins: []string{"https://food.example"}, SecureCookies: true, CSRFKey: strings.Repeat("c", 32), ServiceKey: strings.Repeat("s", 32), Timeout: 5 * time.Second}, slog.New(slog.NewJSONHandler(io.Discard, nil)), f.repo.Ready)
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Public(f.jwt.PublicKeys())
	send := func(method, path, body, access, csrf, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := send("POST", "/v1/auth/register", `{"email":"http@example.test","password":"`+password+`","roles":["admin"]}`, "", "", "https://food.example", nil); w.Code != 400 {
		t.Fatal("role injection", w.Code)
	}
	for i := 0; i < 2; i++ {
		w := send("POST", "/v1/auth/register", `{"email":"http@example.test","password":"`+password+`"}`, "", "", "https://food.example", nil)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := send("POST", "/v1/auth/login", `{"email":"http@example.test","password":"`+password+`"}`, "", "", "https://food.example", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var login struct {
		Access string `json:"access_token"`
		CSRF   string `json:"csrf_token"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	if strings.Contains(w.Body.String(), cookie.Value) || !cookie.Secure || !cookie.HttpOnly {
		t.Fatal("refresh credential leaked")
	}
	if got := send("GET", "/v1/auth/me", "", login.Access, "", "", nil); got.Code != 200 || strings.Contains(got.Body.String(), "password") {
		t.Fatal(got.Code, got.Body)
	}
	if got := send("GET", "/v1/auth/me?user_id=1", "", login.Access, "", "", nil); got.Code != 200 || !strings.Contains(got.Body.String(), `"customer"`) {
		t.Fatal("profile trusts query")
	}
	for _, tc := range []struct {
		method, path, access, csrf, origin string
		status                             int
	}{{"GET", "/v1/auth/me", "", "", "", 401}, {"POST", "/v1/auth/me", login.Access, "", "https://food.example", 405}, {"POST", "/v1/auth/refresh", "", "", "https://food.example", 403}, {"POST", "/v1/auth/refresh", "", login.CSRF, "https://evil.example", 403}, {"POST", "/v1/auth/refresh", "", login.CSRF, "", 403}} {
		got := send(tc.method, tc.path, `{}`, tc.access, tc.csrf, tc.origin, cookie)
		if got.Code != tc.status {
			t.Fatal(tc.path, got.Code, tc.status)
		}
	}
	preflight := httptest.NewRequest("OPTIONS", "/v1/auth/refresh", nil)
	preflight.Header.Set("Origin", "https://food.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "Content-Type,X-CSRF-Token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, preflight)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("credential CORS", rec.Code)
	}
	internal := httptest.NewServer(h.Internal())
	defer internal.Close()
	jwks := httptest.NewServer(handler)
	defer jwks.Close()
	verifier, err := security.New(security.Config{Environment: "test", Issuer: "https://auth.example.test", Audience: "food-api", JWKSURL: jwks.URL + "/.well-known/jwks.json", AuthServiceURL: internal.URL, AuthServiceKey: strings.Repeat("s", 32)})
	if err != nil {
		t.Fatal(err)
	}
	protected := verifier.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := security.FromContext(r.Context())
		if !ok || p.UserID == 0 || p.CustomerID == 0 {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(204)
	}))
	check := func(expected int) {
		t.Helper()
		req := httptest.NewRequest("GET", "/business", nil)
		req.Header.Set("Authorization", "Bearer "+login.Access)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, req)
		if w.Code != expected {
			t.Fatal("live business authority", w.Code, expected)
		}
	}
	check(204)
	got := send("POST", "/v1/auth/logout", `{}`, "", login.CSRF, "https://food.example", cookie)
	if got.Code != 204 {
		t.Fatal(got.Code, got.Body)
	}
	check(401)
	if _, _, err = f.svc.Authenticate(ctx, login.Access); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("HTTP logout retained authority")
	}
}

func TestCleanupAndRevocationLeasesAreSafeAcrossWorkers(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	f.register(t, "cleanup@example.test")
	old := f.login(t, "cleanup@example.test")
	active := f.login(t, "cleanup@example.test")
	if _, err := f.repo.pool.Exec(ctx, `UPDATE auth_sessions SET created_at=clock_timestamp()-interval '10 days',expires_at=clock_timestamp()-interval '8 days' WHERE id=$1`, old.Session.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.repo.Prune(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	f.authenticate(t, active)
	var n int
	if err := f.repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_refresh_tokens WHERE session_id=$1`, old.Session.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("cleanup kept old token", err)
	}
	if _, err := f.repo.pool.Exec(ctx, `INSERT INTO auth_device_revocations(user_id) VALUES($1)`, active.User.ID); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := f.repo.ClaimRevocation(ctx, "lease-one")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, err = f.repo.ClaimRevocation(ctx, "lease-two"); err != nil || ok {
		t.Fatal("live lease stolen", err)
	}
	if _, err = f.repo.pool.Exec(ctx, `UPDATE auth_device_revocations SET not_after=clock_timestamp(),lease_token=NULL,lease_until=NULL WHERE user_id=$1`, active.User.ID); err != nil {
		t.Fatal(err)
	}
	next, ok, err := f.repo.ClaimRevocation(ctx, "lease-new")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = f.repo.FinishRevocation(ctx, lease, true); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.pool.QueryRow(ctx, `SELECT count(*) FROM auth_device_revocations`).Scan(&n); err != nil || n != 1 {
		t.Fatal("stale worker removed newer work", err)
	}
	if err = f.repo.FinishRevocation(ctx, next, true); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDisableAndEmailChangeDoNotInventVerification(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	if err := f.admin.Provision(ctx, domain.User{Email: "only-admin@example.test", Roles: []string{"admin"}}, password, "bootstrap-admin"); err != nil {
		t.Fatal(err)
	}
	admin := f.login(t, "only-admin@example.test")
	if err := f.admin.Disable(ctx, admin.User.ID, "test-admin"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("last admin disabled", err)
	}
	f.register(t, "change@example.test")
	u, err := f.repo.ByEmail(ctx, "change@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.pool.Exec(ctx, `UPDATE auth_users SET email_verified_at=clock_timestamp() WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	u.Email = "changed@example.test"
	if err = f.admin.Provision(ctx, u, password, "test-admin"); err != nil {
		t.Fatal(err)
	}
	changed, err := f.repo.ByEmail(ctx, u.Email)
	if err != nil || changed.EmailVerifiedAt != nil {
		t.Fatal("verification transferred to new address", err)
	}
	session := f.login(t, u.Email)
	if err = f.admin.Disable(ctx, u.ID, "test-admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.svc.Authenticate(ctx, session.AccessToken); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("disabled user kept access", err)
	}
	if _, err = f.svc.Login(ctx, u.Email, password, "peer"); !errors.Is(err, domain.ErrCredentials) {
		t.Fatal("disabled user can log in", err)
	}
}
