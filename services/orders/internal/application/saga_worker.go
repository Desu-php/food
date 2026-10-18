package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/Desu-php/food/services/orders/internal/domain"
)

type SagaRepository interface {
	StepSaga(context.Context, time.Time) (domain.Saga, bool, error)
}
type SagaWorker struct {
	repo   SagaRepository
	poll   time.Duration
	logger *slog.Logger
}

func NewSagaWorker(repo SagaRepository, poll time.Duration, logger *slog.Logger) *SagaWorker {
	return &SagaWorker{repo, poll, logger}
}
func (w *SagaWorker) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		op, cancel := context.WithTimeout(ctx, 5*time.Second)
		s, found, err := w.repo.StepSaga(op, time.Now())
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			w.logger.Error("saga processing failed", "error_type", "storage_or_transition_failure")
		}
		if found && err == nil {
			logger := w.logger.With("order_id", s.OrderID, "saga_state", s.State, "saga_version", s.Version, "correlation_id", s.CorrelationID, "attempt", s.Attempts)
			if s.State == domain.SagaNeedsAttention {
				logger.Error("saga requires operator intervention", "reason", s.Reason)
			} else {
				logger.Info("saga scheduled action committed")
			}
		}
		if !found || err != nil {
			timer := time.NewTimer(w.poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
	return nil
}
