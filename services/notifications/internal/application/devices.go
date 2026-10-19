package application

import (
	"context"

	"github.com/Desu-php/food/services/notifications/internal/domain"
)

type DeviceRepository interface {
	Register(context.Context, domain.DeviceRegistration) (domain.DeviceRegistration, error)
	Devices(context.Context, int64) ([]domain.DeviceRegistration, error)
	Deactivate(context.Context, int64, int64) error
}
type Devices struct{ repo DeviceRepository }

func NewDevices(repo DeviceRepository) *Devices { return &Devices{repo} }
func (s *Devices) Register(ctx context.Context, d domain.DeviceRegistration) (domain.DeviceRegistration, error) {
	if err := d.Validate(); err != nil {
		return d, err
	}
	return s.repo.Register(ctx, d)
}
func (s *Devices) List(ctx context.Context, user int64) ([]domain.DeviceRegistration, error) {
	return s.repo.Devices(ctx, user)
}
func (s *Devices) Deactivate(ctx context.Context, user, id int64) error {
	if user <= 0 || id <= 0 {
		return domain.ErrNotFound
	}
	return s.repo.Deactivate(ctx, user, id)
}
