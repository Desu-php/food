package application

import (
	"context"

	"github.com/Desu-php/food/services/catalog/internal/domain"
)

type RestaurantPricing struct {
	Settings domain.DeliverySettings `json:"settings"`
	Tariffs  []domain.DeliveryTariff `json:"tariffs"`
}
type PricingRepository interface {
	GetPricing(context.Context, int64) (RestaurantPricing, error)
	UpdateSettings(context.Context, int64, domain.DeliverySettings) (RestaurantPricing, error)
	SaveTariff(context.Context, domain.DeliveryTariff) (domain.DeliveryTariff, error)
	DeleteTariff(context.Context, int64, int64) error
}
type Pricing struct{ repo PricingRepository }

func NewPricing(repo PricingRepository) *Pricing { return &Pricing{repo: repo} }
func (s *Pricing) Get(ctx context.Context, id int64) (RestaurantPricing, error) {
	if err := domain.ValidateID(id); err != nil {
		return RestaurantPricing{}, err
	}
	return s.repo.GetPricing(ctx, id)
}
func (s *Pricing) Update(ctx context.Context, id int64, settings domain.DeliverySettings) (RestaurantPricing, error) {
	if err := domain.ValidateID(id); err != nil {
		return RestaurantPricing{}, err
	}
	if settings.MinimumDeliverySubtotalMinor < 0 {
		return RestaurantPricing{}, domain.ErrMinimumSubtotal
	}
	return s.repo.UpdateSettings(ctx, id, settings)
}
func (s *Pricing) SaveTariff(ctx context.Context, t domain.DeliveryTariff) (domain.DeliveryTariff, error) {
	if err := domain.ValidateID(t.RestaurantID); err != nil {
		return t, err
	}
	if t.ID < 0 {
		return t, domain.ErrTariff
	}
	if err := t.Validate(); err != nil {
		return t, err
	}
	return s.repo.SaveTariff(ctx, t)
}
func (s *Pricing) DeleteTariff(ctx context.Context, restaurant, id int64) error {
	if restaurant <= 0 || id <= 0 {
		return domain.ErrTariff
	}
	return s.repo.DeleteTariff(ctx, restaurant, id)
}
