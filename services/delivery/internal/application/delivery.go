package application

import (
	"context"
	"slices"
	"time"

	events "example.com/food/contracts/delivery/v1"
	"example.com/food/delivery/internal/domain"
)

type Repository interface {
	CreateCourier(context.Context, domain.Courier) (domain.Courier, error)
	SetCourierAvailability(context.Context, int64, string) (domain.Courier, error)
	AvailableCouriers(context.Context, int64, int) ([]domain.Courier, error)
	Couriers(context.Context, int64, int) ([]domain.Courier, error)
	List(context.Context, int64, int64, int) ([]domain.Delivery, error)
	Get(context.Context, int64, int64) (domain.Delivery, error)
	Change(context.Context, int64, string, func(*domain.Delivery) error) (domain.Delivery, error)
}
type Delivery struct {
	repo        Repository
	dispatchers []int64
}

func NewDelivery(repo Repository, dispatchers []int64) *Delivery {
	return &Delivery{repo, slices.Clone(dispatchers)}
}
func (s *Delivery) dispatch(user int64) error {
	if user <= 0 || !slices.Contains(s.dispatchers, user) {
		return domain.ErrForbidden
	}
	return nil
}
func (s *Delivery) CreateCourier(ctx context.Context, user int64, name string) (domain.Courier, error) {
	if err := s.dispatch(user); err != nil {
		return domain.Courier{}, err
	}
	c, err := domain.NewCourier(name, time.Now())
	if err != nil {
		return c, err
	}
	return s.repo.CreateCourier(ctx, c)
}
func validPage(after int64, limit int) bool { return after >= 0 && limit >= 1 && limit <= 100 }
func (s *Delivery) Couriers(ctx context.Context, user, after int64, limit int, available ...bool) ([]domain.Courier, error) {
	if err := s.dispatch(user); err != nil {
		return nil, err
	}
	if !validPage(after, limit) {
		return nil, domain.ErrInvalid
	}
	if len(available) > 0 && available[0] {
		return s.repo.AvailableCouriers(ctx, after, limit)
	}
	return s.repo.Couriers(ctx, after, limit)
}

// Dispatcher reads are unrestricted within the service; courier reads are scoped to assignment.
func (s *Delivery) List(ctx context.Context, user, courier, after int64, limit int) ([]domain.Delivery, error) {
	if courier <= 0 {
		if err := s.dispatch(user); err != nil {
			return nil, err
		}
	}
	if !validPage(after, limit) {
		return nil, domain.ErrInvalid
	}
	return s.repo.List(ctx, courier, after, limit)
}
func (s *Delivery) Get(ctx context.Context, user, courier, id int64) (domain.Delivery, error) {
	if courier <= 0 {
		if err := s.dispatch(user); err != nil {
			return domain.Delivery{}, err
		}
	}
	if id <= 0 {
		return domain.Delivery{}, domain.ErrInvalid
	}
	return s.repo.Get(ctx, courier, id)
}
func (s *Delivery) Assign(ctx context.Context, user, id, courier int64) (domain.Delivery, error) {
	if err := s.dispatch(user); err != nil {
		return domain.Delivery{}, err
	}
	if id <= 0 || courier <= 0 {
		return domain.Delivery{}, domain.ErrInvalid
	}
	return s.repo.Change(ctx, id, events.CourierAssigned, func(d *domain.Delivery) error { return d.Assign(courier, time.Now()) })
}
func (s *Delivery) Pickup(ctx context.Context, courier, id int64) (domain.Delivery, error) {
	if courier <= 0 {
		return domain.Delivery{}, domain.ErrForbidden
	}
	if id <= 0 {
		return domain.Delivery{}, domain.ErrInvalid
	}
	return s.repo.Change(ctx, id, events.DeliveryPickedUp, func(d *domain.Delivery) error { return d.Pickup(courier, time.Now()) })
}
func (s *Delivery) Complete(ctx context.Context, courier, id int64, confirmed bool, reference string) (domain.Delivery, error) {
	if courier <= 0 {
		return domain.Delivery{}, domain.ErrForbidden
	}
	if id <= 0 {
		return domain.Delivery{}, domain.ErrInvalid
	}
	return s.repo.Change(ctx, id, events.DeliveryCompleted, func(d *domain.Delivery) error { return d.Complete(courier, confirmed, reference, time.Now()) })
}

func (s *Delivery) SetAvailability(ctx context.Context, courier, id int64, status string) (domain.Courier, error) {
	if courier <= 0 || courier != id {
		return domain.Courier{}, domain.ErrForbidden
	}
	return s.repo.SetCourierAvailability(ctx, id, status)
}
func (s *Delivery) Decline(ctx context.Context, courier, id int64) (domain.Delivery, error) {
	if courier <= 0 {
		return domain.Delivery{}, domain.ErrForbidden
	}
	if id <= 0 {
		return domain.Delivery{}, domain.ErrInvalid
	}
	return s.repo.Change(ctx, id, events.CourierReleased, func(d *domain.Delivery) error { return d.Decline(courier, time.Now()) })
}
