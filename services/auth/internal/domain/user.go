package domain

import (
	"errors"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("invalid input")
	ErrCredentials = errors.New("invalid credentials")
	ErrForbidden   = errors.New("access denied")
	ErrLimited     = errors.New("temporarily rate limited")
	ErrUnavailable = errors.New("operation unavailable")
	ErrConflict    = errors.New("identity conflict")
)

const (
	Active              = "ACTIVE"
	PendingVerification = "PENDING_VERIFICATION"
	Disabled            = "DISABLED"
)

type User struct {
	ID                                   int64
	Subject, Email, PasswordHash, Status string
	CustomerID, CourierID, Version       int64
	Roles, Permissions                   []string
	Restaurants                          []int64
	EmailVerifiedAt                      *time.Time
	CreatedAt, UpdatedAt                 time.Time
}

func Email(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || !utf8.ValidString(value) || !strings.Contains(value, "@") {
		return "", ErrInvalid
	}
	return value, nil
}

// Passphrases are accepted without arbitrary character-class requirements.
func ValidatePassword(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 15 || len(value) > 1024 {
		return ErrInvalid
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return ErrInvalid
		}
	}
	lower := strings.ToLower(value)
	if strings.TrimSpace(value) == "" || slices.Contains([]string{"passwordpassword", "123456789012345", "qwertyqwertyqwerty", "letmeinletmeinletmein"}, lower) {
		return ErrInvalid
	}
	first, same := rune(0), true
	for i, r := range value {
		if i == 0 {
			first = r
		} else if first != r {
			same = false
		}
	}
	if same {
		return ErrInvalid
	}
	return nil
}

func (u User) ValidateBindings() error {
	if u.ID < 0 || u.Subject == "" || len(u.Subject) > 255 || u.CustomerID < 0 || u.CourierID < 0 {
		return ErrInvalid
	}
	for _, role := range u.Roles {
		if !slices.Contains([]string{"customer", "restaurant_staff", "courier", "dispatcher", "admin"}, role) {
			return ErrInvalid
		}
	}
	if slices.Contains(u.Roles, "customer") && u.CustomerID <= 0 || slices.Contains(u.Roles, "courier") && u.CourierID <= 0 {
		return ErrInvalid
	}
	if len(u.Restaurants) > 0 && !slices.Contains(u.Roles, "restaurant_staff") && !slices.Contains(u.Roles, "dispatcher") && !slices.Contains(u.Roles, "admin") {
		return ErrInvalid
	}
	for _, id := range u.Restaurants {
		if id <= 0 {
			return ErrInvalid
		}
	}
	for _, p := range u.Permissions {
		if !slices.Contains([]string{"delivery:dispatch", "saga:retry"}, p) {
			return ErrInvalid
		}
	}
	return nil
}

type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Claims struct {
	Subject, SessionID string
	Version            int64
	ExpiresAt          time.Time
}
