package application

import (
	"context"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func (s *Delivery) WithDispatch(d *Dispatch) *Delivery { s.automatic = d; return s }
func (s *Delivery) UpdateLocation(ctx context.Context, courier int64, p geo.Point) error {
	if s.automatic == nil {
		return ErrLocationUnavailable
	}
	return s.automatic.UpdateLocation(ctx, courier, p)
}
func (s *Delivery) RetryDispatch(ctx context.Context, user, id int64) error {
	if err := s.dispatch(user); err != nil {
		return err
	}
	if id <= 0 {
		return domain.ErrInvalid
	}
	if s.automatic == nil {
		return ErrLocationUnavailable
	}
	return s.automatic.Retry(ctx, id)
}
