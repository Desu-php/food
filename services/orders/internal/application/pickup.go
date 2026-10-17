package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"example.com/food/orders/internal/domain"
)

var (
	ErrPickupCode        = errors.New("invalid or expired pickup code")
	ErrPickupRateLimited = errors.New("pickup verification temporarily blocked")
)

// PickupCompletion carries only staff input. Amount, currency, time and staff ID
// are set from the locked order and the authenticated actor, never from JSON.
type PickupCompletion struct {
	Code             string
	PaymentConfirmed bool
	ReceiptReference string
}

type PickupCode struct {
	Code      string    `json:"pickup_code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Orders) IssuePickupCode(ctx context.Context, customer, id int64) (PickupCode, error) {
	if customer <= 0 || id <= 0 {
		return PickupCode{}, ErrInvalidRequest
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return PickupCode{}, err
	}
	code := hex.EncodeToString(token)
	hash := sha256.Sum256([]byte(code))
	expires := time.Now().UTC().Add(24 * time.Hour)
	if err := s.repo.IssuePickupCode(ctx, customer, id, hash[:], expires); err != nil {
		return PickupCode{}, err
	}
	return PickupCode{code, expires}, nil
}

func (s *Restaurants) CompletePickup(ctx context.Context, user, restaurant, id int64, c PickupCompletion) (domain.Order, error) {
	if err := s.authorize(user, restaurant); err != nil {
		return domain.Order{}, err
	}
	if id <= 0 {
		return domain.Order{}, ErrInvalidRequest
	}
	if err := validatePickup(c); err != nil {
		return domain.Order{}, err
	}
	return s.repo.CompletePickup(ctx, user, restaurant, id, c)
}

func validatePickup(c PickupCompletion) error {
	if len(c.Code) != 32 || !utf8.ValidString(c.ReceiptReference) || strings.ContainsRune(c.ReceiptReference, 0) || utf8.RuneCountInString(c.ReceiptReference) > 200 || strings.TrimSpace(c.ReceiptReference) != c.ReceiptReference {
		return ErrInvalidRequest
	}
	if _, err := hex.DecodeString(c.Code); err != nil {
		return ErrPickupCode
	}
	return nil
}

// VerifyPickup allows staff to identify the customer before taking physical POS payment.
// Completion always re-verifies the token while holding the same order lock.
func (s *Restaurants) VerifyPickup(ctx context.Context, user, restaurant, id int64, code string) (domain.Order, error) {
	if err := s.authorize(user, restaurant); err != nil {
		return domain.Order{}, err
	}
	if id <= 0 {
		return domain.Order{}, ErrInvalidRequest
	}
	if err := validatePickup(PickupCompletion{Code: code}); err != nil {
		return domain.Order{}, err
	}
	return s.repo.VerifyPickup(ctx, restaurant, id, code)
}
