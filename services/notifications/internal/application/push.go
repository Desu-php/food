package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/Desu-php/food/services/notifications/internal/domain"
)

type PushProvider interface {
	Send(context.Context, string, domain.PushMessage) domain.ProviderResult
}
type OfferRelevance interface {
	Live(context.Context, int64, int64) (bool, error)
}
type PushRepository interface {
	Expand(context.Context) (bool, error)
	ClaimPush(context.Context, PushConfig) (domain.Delivery, bool, error)
	Current(context.Context, domain.Delivery) (string, bool, error)
	FinishPush(context.Context, domain.Delivery, domain.ProviderResult, time.Duration, int) error
	PruneDevices(context.Context, time.Duration) error
}
type PushConfig struct {
	Lease, Timeout, DBTimeout, PollInterval, RetryBase, RetryMax, SendInterval, StaleAfter, CleanupInterval time.Duration
	MaxAttempts                                                                                             int
}
type PushWorker struct {
	repo     PushRepository
	provider PushProvider
	offers   OfferRelevance
	cfg      PushConfig
	logger   *slog.Logger
}

func NewPushWorker(repo PushRepository, provider PushProvider, offers OfferRelevance, cfg PushConfig, logger *slog.Logger) *PushWorker {
	return &PushWorker{repo, provider, offers, cfg, logger}
}
func (w *PushWorker) Step(ctx context.Context) (bool, error) {
	op, cancel := context.WithTimeout(ctx, w.cfg.DBTimeout)
	expanded, err := w.repo.Expand(op)
	cancel()
	if err != nil {
		return false, err
	}
	op, cancel = context.WithTimeout(ctx, w.cfg.DBTimeout)
	d, ok, err := w.repo.ClaimPush(op, w.cfg)
	cancel()
	if err != nil || !ok {
		return expanded, err
	}
	result := domain.ProviderResult{Code: "cancelled"}
	op, cancel = context.WithTimeout(ctx, w.cfg.DBTimeout)
	target, current, err := w.repo.Current(op, d)
	cancel()
	if err != nil {
		return true, err
	}
	if current && d.EventType == "CourierOfferCreated" {
		op, cancel = context.WithTimeout(ctx, w.cfg.Timeout)
		current, err = w.offers.Live(op, d.OfferID, d.Recipient.ID)
		cancel()
		if err != nil {
			result = domain.ProviderResult{Code: "relevance_unavailable", Temporary: true}
		}
	}
	if current && err == nil {
		// Recheck after remote relevance I/O; no SQL transaction spans provider calls.
		op, cancel = context.WithTimeout(ctx, w.cfg.DBTimeout)
		target, current, err = w.repo.Current(op, d)
		cancel()
		if err != nil {
			return true, err
		}
		if current {
			op, cancel = context.WithTimeout(ctx, w.cfg.Timeout)
			result = w.provider.Send(op, target, d.Message)
			cancel()
		}
	}
	base, maximum := w.cfg.RetryBase, w.cfg.RetryMax
	if d.EventType == "CourierOfferCreated" {
		base = min(base, time.Second)
		maximum = min(maximum, 5*time.Second)
	}
	delay := retryDelay(d.Attempts, base, maximum)
	if result.RetryAfter > delay {
		delay = result.RetryAfter
	}
	op, cancel = context.WithTimeout(ctx, w.cfg.DBTimeout)
	err = w.repo.FinishPush(op, d, result, delay, w.cfg.MaxAttempts)
	cancel()
	w.logger.Info("push attempt completed", "event_id", d.EventID, "correlation_id", d.CorrelationID, "notification_id", d.NotificationID, "delivery_id", d.ID, "attempt", d.Attempts, "result", result.Code, "temporary", result.Temporary)
	return true, err
}
func (w *PushWorker) Run(ctx context.Context) error {
	cleanup := time.NewTicker(w.cfg.CleanupInterval)
	defer cleanup.Stop()
	for ctx.Err() == nil {
		select {
		case <-cleanup.C:
			op, cancel := context.WithTimeout(ctx, w.cfg.DBTimeout)
			err := w.repo.PruneDevices(op, w.cfg.StaleAfter)
			cancel()
			if err != nil {
				w.logger.Error("device cleanup failed", "error_type", "database")
			}
		default:
		}
		worked, err := w.Step(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			w.logger.Error("push processing failed", "error_type", "database_or_lease")
		}
		if !worked || err != nil {
			timer := time.NewTimer(w.cfg.PollInterval)
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
