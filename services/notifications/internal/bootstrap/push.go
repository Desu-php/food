package bootstrap

import (
	"context"
	"errors"
	"github.com/Desu-php/food/platform/security"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	delivery "github.com/Desu-php/food/services/notifications/internal/adapters/delivery"
	"github.com/Desu-php/food/services/notifications/internal/adapters/fcm"
	"github.com/Desu-php/food/services/notifications/internal/adapters/postgres"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PushConfig struct {
	Worker                                                      application.PushConfig
	DatabaseURL, Provider, ProjectID, DeliveryURL, RelevanceKey string
}

func LoadPushConfig() (PushConfig, error) {
	c := PushConfig{DatabaseURL: os.Getenv("DATABASE_URL"), Provider: env("PUSH_PROVIDER", "fcm"), ProjectID: os.Getenv("FIREBASE_PROJECT_ID"), DeliveryURL: env("DELIVERY_INTERNAL_URL", "http://localhost:8085"), RelevanceKey: os.Getenv("PUSH_RELEVANCE_KEY")}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL required")
	}
	if c.Provider != "fcm" && c.Provider != "fake" {
		return c, errors.New("invalid PUSH_PROVIDER")
	}
	if c.Provider == "fake" && env("APP_ENV", "production") != "development" && env("APP_ENV", "production") != "test" {
		return c, errors.New("fake push forbidden outside development/test")
	}
	if c.Provider == "fcm" && c.ProjectID == "" {
		return c, errors.New("FIREBASE_PROJECT_ID required")
	}
	if len(c.RelevanceKey) < 32 || env("APP_ENV", "production") == "production" && strings.HasPrefix(c.RelevanceKey, "local-only-") {
		return c, errors.New("PUSH_RELEVANCE_KEY must contain at least 32 bytes")
	}
	u, err := url.Parse(c.DeliveryURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("invalid DELIVERY_INTERNAL_URL")
	}
	for _, v := range []struct {
		key, defaultValue string
		target            *time.Duration
	}{
		{"PUSH_LEASE", "45s", &c.Worker.Lease}, {"PUSH_TIMEOUT", "10s", &c.Worker.Timeout}, {"DB_OPERATION_TIMEOUT", "5s", &c.Worker.DBTimeout}, {"PUSH_POLL_INTERVAL", "250ms", &c.Worker.PollInterval}, {"PUSH_RETRY_BASE", "1m", &c.Worker.RetryBase}, {"PUSH_RETRY_MAX", "1h", &c.Worker.RetryMax}, {"PUSH_SEND_INTERVAL", "100ms", &c.Worker.SendInterval}, {"DEVICE_STALE_AFTER", "720h", &c.Worker.StaleAfter}, {"DEVICE_CLEANUP_INTERVAL", "1h", &c.Worker.CleanupInterval},
	} {
		d, err := time.ParseDuration(env(v.key, v.defaultValue))
		if err != nil || d < time.Millisecond || d > 8760*time.Hour {
			return c, errors.New("invalid " + v.key)
		}
		*v.target = d
	}
	n, err := strconv.Atoi(env("PUSH_MAX_ATTEMPTS", "8"))
	if err != nil || n < 1 || n > 30 {
		return c, errors.New("invalid PUSH_MAX_ATTEMPTS")
	}
	c.Worker.MaxAttempts = n
	if c.Worker.Lease <= 2*c.Worker.Timeout+3*c.Worker.DBTimeout || c.Worker.RetryBase > c.Worker.RetryMax || c.Worker.SendInterval < time.Millisecond {
		return c, errors.New("invalid push lease or retry bounds")
	}
	return c, nil
}
func RunPush(ctx context.Context, c PushConfig, logger *slog.Logger) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = c.Worker.DBTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("initialize push database failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, c.Worker.DBTimeout)
	_, err = pool.Exec(check, `SELECT claim_token FROM push_deliveries LIMIT 0;SELECT installation_id FROM device_registrations LIMIT 0`)
	cancel()
	if err != nil {
		return errors.New("push startup check failed: check migrations")
	}
	store := postgres.NewPushStore(pool)
	if os.Getenv("AUTH_PRINCIPAL_URL") != "" {
		authcfg, err := security.Load()
		if err != nil {
			return err
		}
		authority, err := security.NewAuthority(authcfg.AuthServiceURL, authcfg.AuthServiceKey, authcfg.Environment)
		if err != nil {
			return err
		}
		store.WithAuthority(authority)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, c.Worker.DBTimeout)
	err = store.PruneDevices(cleanupCtx, c.Worker.StaleAfter)
	cleanupCancel()
	if err != nil {
		return errors.New("initial device cleanup failed")
	}
	var provider application.PushProvider = fcm.Fake{}
	if c.Provider == "fcm" {
		// ADC token sources must retain the worker lifetime context; cancelling a
		// constructor context can break later credential refresh. Send is bounded separately.
		provider, err = fcm.New(ctx, c.ProjectID)
		if err != nil {
			return err
		}
	}
	client := &http.Client{Timeout: c.Worker.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	relevance := &delivery.Relevance{BaseURL: c.DeliveryURL, Key: c.RelevanceKey, Client: client}
	logger.Info("push worker started", "provider", c.Provider)
	return application.NewPushWorker(store, provider, relevance, c.Worker, logger).Run(ctx)
}
