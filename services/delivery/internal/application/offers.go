package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type OfferRepository interface {
	Offers(context.Context, int64) ([]domain.CourierOffer, error)
	RespondOffer(context.Context, int64, int64, bool) (domain.CourierOffer, error)
	ExpireOffer(context.Context) (bool, error)
}

type Offers struct{ repo OfferRepository }

func NewOffers(repo OfferRepository) *Offers            { return &Offers{repo} }
func (s *Delivery) WithOffers(offers *Offers) *Delivery { s.offers = offers; return s }
func (s *Delivery) Offers(ctx context.Context, courier int64) ([]domain.CourierOffer, error) {
	if courier <= 0 {
		return nil, domain.ErrForbidden
	}
	return s.offers.repo.Offers(ctx, courier)
}
func (s *Delivery) RespondOffer(ctx context.Context, courier, id int64, accept bool) (domain.CourierOffer, error) {
	if courier <= 0 {
		return domain.CourierOffer{}, domain.ErrForbidden
	}
	if id <= 0 {
		return domain.CourierOffer{}, domain.ErrInvalid
	}
	return s.offers.repo.RespondOffer(ctx, courier, id, accept)
}

// Expiry runs independently of Redis and of whether automatic search is enabled.
func (s *Offers) RunExpiry(ctx context.Context, logger *slog.Logger) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		op, cancel := context.WithTimeout(ctx, 3*time.Second)
		found, err := s.repo.ExpireOffer(op)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("courier offer expiry failed", "error_type", "storage_or_internal")
		}
		delay := time.Second
		if found && err == nil {
			delay = 10 * time.Millisecond
		}
		timer.Reset(delay)
	}
}
