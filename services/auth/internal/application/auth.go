package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Desu-php/food/services/auth/internal/domain"
)

type Repository interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
	Register(context.Context, domain.User) error
	ByEmail(context.Context, string) (domain.User, error)
	StartSession(context.Context, domain.User, domain.Session, string) (domain.User, error)
	Rotate(context.Context, string, string) (domain.User, domain.Session, error)
	Authenticate(context.Context, domain.Claims) (domain.User, error)
	Logout(context.Context, domain.Claims, bool) error
	LogoutToken(context.Context, string) error
	ChangePassword(context.Context, domain.Claims, string, string) error
	Sessions(context.Context, domain.Claims) ([]domain.Session, error)
	RevokeSession(context.Context, domain.Claims, string) error
	UpdateBindings(context.Context, domain.Claims, int64, domain.User) error
	DeviceAuthorized(context.Context, int64, string, int64) (bool, error)
}
type Passwords interface {
	Hash(string) (string, error)
	Verify(string, string) bool
}
type Tokens interface {
	Sign(domain.User, domain.Session) (string, time.Time, error)
	Verify(string) (domain.Claims, error)
}
type Config struct {
	SessionTTL      time.Duration
	AllowUnverified bool
}
type Auth struct {
	repo      Repository
	passwords Passwords
	tokens    Tokens
	cfg       Config
	dummy     string
}
type Result struct {
	AccessToken  string
	ExpiresAt    time.Time
	RefreshToken string
	User         domain.User
	Session      domain.Session
}

func New(repo Repository, passwords Passwords, tokens Tokens, cfg Config) (*Auth, error) {
	if cfg.SessionTTL < time.Minute || cfg.SessionTTL > 90*24*time.Hour {
		return nil, domain.ErrInvalid
	}
	dummy, err := passwords.Hash("dummy-only-" + Secret())
	if err != nil {
		return nil, err
	}
	return &Auth{repo, passwords, tokens, cfg, dummy}, nil
}
func Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("operating system randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (s *Auth) Limit(ctx context.Context, key string, limit int, window time.Duration) error {
	ok, err := s.repo.Allow(ctx, Hash(key), limit, window)
	if err != nil {
		return fmt.Errorf("authentication rate limit: %w", err)
	}
	if !ok {
		return domain.ErrLimited
	}
	return nil
}
func (s *Auth) Register(ctx context.Context, email, password, peer string) error {
	if err := s.Limit(ctx, "register:peer:"+peer, 10, time.Hour); err != nil {
		return err
	}
	email, err := domain.Email(email)
	if err != nil {
		return err
	}
	if err = domain.ValidatePassword(password); err != nil {
		return err
	}
	if err = s.Limit(ctx, "register:email:"+email, 3, time.Hour); err != nil {
		return err
	}
	hash, err := s.passwords.Hash(password)
	if err != nil {
		return err
	}
	status := domain.PendingVerification
	if s.cfg.AllowUnverified {
		status = domain.Active
	}
	return s.repo.Register(ctx, domain.User{Subject: Secret(), Email: email, PasswordHash: hash, Status: status, Roles: []string{"customer"}, Permissions: []string{}, Restaurants: []int64{}})
}
func (s *Auth) Login(ctx context.Context, email, password, peer string) (Result, error) {
	if err := s.Limit(ctx, "login:peer:"+peer, 30, time.Minute); err != nil {
		return Result{}, err
	}
	email, err := domain.Email(email)
	if err != nil {
		return Result{}, domain.ErrCredentials
	}
	if err = s.Limit(ctx, "login:email:"+email, 10, 15*time.Minute); err != nil {
		return Result{}, err
	}
	u, err := s.repo.ByEmail(ctx, email)
	if err != nil && !errors.Is(err, domain.ErrCredentials) {
		return Result{}, err
	}
	hash := u.PasswordHash
	if hash == "" {
		hash = s.dummy
	}
	valid := len(password) <= 1024 && s.passwords.Verify(hash, password)
	if !valid || err != nil || u.Status != domain.Active {
		return Result{}, domain.ErrCredentials
	}
	raw := Secret()
	session := domain.Session{ID: Secret(), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(s.cfg.SessionTTL)}
	u, err = s.repo.StartSession(ctx, u, session, Hash(raw))
	if err != nil {
		return Result{}, err
	}
	return s.result(u, session, raw)
}
func (s *Auth) result(u domain.User, session domain.Session, refresh string) (Result, error) {
	access, expires, err := s.tokens.Sign(u, session)
	if err != nil {
		return Result{}, err
	}
	return Result{access, expires, refresh, u, session}, nil
}
func (s *Auth) Refresh(ctx context.Context, raw, peer string) (Result, error) {
	if err := s.Limit(ctx, "refresh:peer:"+peer, 120, time.Minute); err != nil {
		return Result{}, err
	}
	if len(raw) != 43 {
		return Result{}, domain.ErrCredentials
	}
	next := Secret()
	u, session, err := s.repo.Rotate(ctx, Hash(raw), Hash(next))
	if err != nil {
		return Result{}, err
	}
	return s.result(u, session, next)
}
func (s *Auth) Authenticate(ctx context.Context, raw string) (domain.User, domain.Claims, error) {
	c, err := s.tokens.Verify(raw)
	if err != nil {
		return domain.User{}, c, domain.ErrCredentials
	}
	u, err := s.repo.Authenticate(ctx, c)
	return u, c, err
}
func (s *Auth) Logout(ctx context.Context, c domain.Claims, all bool) error {
	return s.repo.Logout(ctx, c, all)
}
func (s *Auth) LogoutToken(ctx context.Context, raw string) error {
	if len(raw) != 43 {
		return domain.ErrCredentials
	}
	return s.repo.LogoutToken(ctx, Hash(raw))
}
func (s *Auth) DeviceAuthorized(ctx context.Context, user int64, kind string, id int64) (bool, error) {
	return s.repo.DeviceAuthorized(ctx, user, kind, id)
}
func (s *Auth) Sessions(ctx context.Context, c domain.Claims) ([]domain.Session, error) {
	return s.repo.Sessions(ctx, c)
}
func (s *Auth) RevokeSession(ctx context.Context, c domain.Claims, id string) error {
	if len(id) != 43 {
		return domain.ErrInvalid
	}
	return s.repo.RevokeSession(ctx, c, id)
}
func (s *Auth) ChangePassword(ctx context.Context, c domain.Claims, current, next string) error {
	if err := s.Limit(ctx, "password:"+c.Subject, 5, 15*time.Minute); err != nil {
		return err
	}
	if err := domain.ValidatePassword(next); err != nil {
		return err
	}
	u, err := s.repo.Authenticate(ctx, c)
	if err != nil {
		return err
	}
	if len(current) > 1024 || !s.passwords.Verify(u.PasswordHash, current) {
		return domain.ErrCredentials
	}
	hash, err := s.passwords.Hash(next)
	if err != nil {
		return err
	}
	return s.repo.ChangePassword(ctx, c, u.PasswordHash, hash)
}
func (s *Auth) UpdateBindings(ctx context.Context, c domain.Claims, id int64, u domain.User) error {
	u = normalized(u)
	if id <= 0 {
		return domain.ErrInvalid
	}
	u.ID = id
	u.Subject = "validated-target"
	if err := u.ValidateBindings(); err != nil {
		return err
	}
	return s.repo.UpdateBindings(ctx, c, id, u)
}
