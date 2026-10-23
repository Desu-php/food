package application

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Desu-php/food/services/orders/internal/domain"
)

var ErrForbidden = errors.New("restaurant access denied")

type RestaurantRepository interface {
	VerifyPickup(context.Context, int64, int64, string) (domain.Order, error)
	CompletePickup(context.Context, int64, int64, int64, PickupCompletion) (domain.Order, error)
	ListIncoming(context.Context, int64, int64, int) ([]domain.Order, error)
	Saga(context.Context, int64, int64) (domain.Saga, error)
	ChangeRestaurant(context.Context, int64, int64, FulfillmentMutation) (domain.Order, error)
}

type Restaurants struct {
	authorization func(context.Context, int64, int64) error
	repo          RestaurantRepository
	grants        map[int64][]int64
	fulfillment   *Fulfillment
}

// Grants are server configuration, never request headers or JSON ownership claims.
func NewRestaurants(repo RestaurantRepository, grants map[int64][]int64, fulfillment ...*Fulfillment) *Restaurants {
	copied := make(map[int64][]int64, len(grants))
	for user, ids := range grants {
		copied[user] = slices.Clone(ids)
	}
	f := NewFulfillment(DefaultSagaConfig())
	if len(fulfillment) > 0 {
		f = fulfillment[0]
	}
	return &Restaurants{repo: repo, grants: copied, fulfillment: f}
}
func (s *Restaurants) WithAuthorization(check func(context.Context, int64, int64) error) *Restaurants {
	s.authorization = check
	return s
}
func (s *Restaurants) authorize(ctx context.Context, user, restaurant int64) error {
	if s.authorization != nil {
		return s.authorization(ctx, user, restaurant)
	}
	if user <= 0 || restaurant <= 0 || !slices.Contains(s.grants[user], restaurant) {
		return ErrForbidden
	}
	return nil
}
func (s *Restaurants) Incoming(ctx context.Context, user, restaurant, after int64, limit int) ([]domain.Order, error) {
	if err := s.authorize(ctx, user, restaurant); err != nil {
		return nil, err
	}
	if after < 0 || limit < 1 || limit > 100 {
		return nil, ErrInvalidRequest
	}
	return s.repo.ListIncoming(ctx, restaurant, after, limit)
}
func (s *Restaurants) Change(ctx context.Context, user, restaurant, id int64, action string) (domain.Order, error) {
	if err := s.authorize(ctx, user, restaurant); err != nil {
		return domain.Order{}, err
	}
	if id <= 0 {
		return domain.Order{}, ErrInvalidRequest
	}
	return s.repo.ChangeRestaurant(ctx, restaurant, id, func(o *domain.Order, saga *domain.Saga) (FulfillmentChange, error) {
		now := time.Now()
		switch action {
		case "accept":
			return s.fulfillment.Accept(ctx, o, saga, now)
		case "reject":
			return s.fulfillment.Cancel(ctx, o, saga, "restaurant_rejected", now)
		case "prepare":
			return s.fulfillment.Prepare(o, saga, now)
		case "ready":
			return s.fulfillment.Ready(o, saga, now)
		case "retry":
			return s.fulfillment.Retry(ctx, o, saga, now)
		default:
			return FulfillmentChange{}, ErrInvalidRequest
		}
	})
}

func (s *Restaurants) GetSaga(ctx context.Context, user, restaurant, id int64) (domain.Saga, error) {
	if err := s.authorize(ctx, user, restaurant); err != nil {
		return domain.Saga{}, err
	}
	if id <= 0 {
		return domain.Saga{}, ErrInvalidRequest
	}
	return s.repo.Saga(ctx, restaurant, id)
}
