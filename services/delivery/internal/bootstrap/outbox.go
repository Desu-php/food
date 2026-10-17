package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	events "example.com/food/contracts/delivery/v1"
	orderevents "example.com/food/contracts/orders/v1"
	httpadapter "example.com/food/delivery/internal/adapters/http"
	"example.com/food/delivery/internal/adapters/kafka"
	"example.com/food/delivery/internal/adapters/postgres"
	"example.com/food/delivery/internal/application"
)

type WorkerConfig struct {
	DatabaseURL string
	HTTPAddr    string
	Brokers     []string
	Topic       string
	LogLevel    slog.Level
	Worker      application.OutboxConfig
}

func LoadWorkerConfig() (WorkerConfig, error) {
	c := WorkerConfig{HTTPAddr: env("OUTBOX_HTTP_ADDR", ":8084"), DatabaseURL: os.Getenv("DATABASE_URL"), Brokers: strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ","), Topic: env("DELIVERY_EVENTS_TOPIC", events.Topic)}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	for i, b := range c.Brokers {
		c.Brokers[i] = strings.TrimSpace(b)
		if c.Brokers[i] == "" {
			return c, errors.New("invalid KAFKA_BROKERS")
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO"))); err != nil {
		return c, errors.New("invalid LOG_LEVEL")
	}
	for _, v := range []struct {
		key, fallback string
		target        *time.Duration
	}{
		{"OUTBOX_POLL_INTERVAL", "500ms", &c.Worker.PollInterval}, {"OUTBOX_LEASE", "30s", &c.Worker.Lease},
		{"KAFKA_PUBLISH_TIMEOUT", "10s", &c.Worker.PublishTimeout}, {"DB_OPERATION_TIMEOUT", "5s", &c.Worker.DBTimeout},
		{"OUTBOX_RETRY_BASE", "1s", &c.Worker.RetryBase}, {"OUTBOX_RETRY_MAX", "5m", &c.Worker.RetryMax},
		{"OUTBOX_RETENTION", "168h", &c.Worker.Retention}, {"OUTBOX_CLEANUP_INTERVAL", "1h", &c.Worker.CleanupInterval},
	} {
		d, err := time.ParseDuration(env(v.key, v.fallback))
		if err != nil || d < time.Millisecond || d > 365*24*time.Hour {
			return c, fmt.Errorf("invalid %s: expected duration between 1ms and 8760h", v.key)
		}
		*v.target = d
	}
	n, err := strconv.Atoi(env("OUTBOX_MAX_ATTEMPTS", "20"))
	if err != nil || n < 1 || n > 1000 {
		return c, errors.New("invalid OUTBOX_MAX_ATTEMPTS")
	}
	c.Worker.MaxAttempts = n
	if c.Worker.Lease <= c.Worker.PublishTimeout+2*c.Worker.DBTimeout {
		return c, errors.New("OUTBOX_LEASE must exceed KAFKA_PUBLISH_TIMEOUT + 2 * DB_OPERATION_TIMEOUT")
	}
	if c.Worker.RetryMax < c.Worker.RetryBase {
		return c, errors.New("OUTBOX_RETRY_MAX must be >= OUTBOX_RETRY_BASE")
	}
	return c, nil
}

func RunWorker(ctx context.Context, c WorkerConfig, logger *slog.Logger, requeue string) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = c.Worker.DBTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("initialize outbox database pool failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, c.Worker.DBTimeout)
	_, err = pool.Exec(check, `SELECT event_id,claim_token,lease_until FROM outbox_events LIMIT 0`)
	cancel()
	if err != nil {
		return errors.New("outbox startup check failed: check connectivity and migrations")
	}
	repo := postgres.NewOutbox(pool)
	if requeue != "" {
		if !orderevents.ValidID(requeue) {
			return errors.New("invalid event UUID")
		}
		dbctx, cancel := context.WithTimeout(ctx, c.Worker.DBTimeout)
		defer cancel()
		if err := repo.Requeue(dbctx, requeue); err != nil {
			return err
		}
		logger.Info("outbox event requeued", "event_id", requeue)
		return nil
	}
	producer, err := kafka.NewProducer(c.Brokers, c.Topic, c.Worker.PublishTimeout)
	if err != nil {
		return err
	}
	defer producer.Close()
	logger.Info("outbox worker started", "topic", c.Topic)
	ready := func(ctx context.Context) error {
		if _, err := pool.Exec(ctx, `SELECT event_id FROM outbox_events LIMIT 0`); err != nil {
			return err
		}
		return producer.Ping(ctx)
	}
	server := &http.Server{Addr: c.HTTPAddr, Handler: httpadapter.NewOutboxHealthHandler(ready), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	listener, err := net.Listen("tcp", c.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 2)
	go func() { done <- serve(runCtx, server, listener, 10*time.Second, logger) }()
	go func() { done <- application.NewOutboxWorker(repo, producer, c.Worker, logger).Run(runCtx) }()
	first := <-done
	stop()
	return errors.Join(first, <-done)
}
