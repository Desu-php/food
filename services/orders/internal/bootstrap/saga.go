package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "example.com/food/orders/internal/adapters/http"
	"example.com/food/orders/internal/adapters/postgres"
	"example.com/food/orders/internal/application"
)

func LoadSagaConfig() (application.SagaConfig, error) {
	c := application.DefaultSagaConfig()
	for _, v := range []struct {
		key    string
		target *time.Duration
	}{{"SAGA_CREATE_TIMEOUT", &c.CreateTimeout}, {"SAGA_ASSIGNMENT_TIMEOUT", &c.AssignmentTimeout}, {"SAGA_PROGRESS_TIMEOUT", &c.ProgressTimeout}, {"SAGA_CANCEL_TIMEOUT", &c.CancelTimeout}, {"SAGA_RETRY_BASE", &c.RetryBase}, {"SAGA_RETRY_MAX", &c.RetryMax}, {"SAGA_POLL_INTERVAL", &c.PollInterval}} {
		d, err := time.ParseDuration(env(v.key, v.target.String()))
		if err != nil || d < time.Millisecond || d > 7*24*time.Hour {
			return c, fmt.Errorf("invalid %s", v.key)
		}
		*v.target = d
	}
	if c.RetryMax < c.RetryBase {
		return c, errors.New("SAGA_RETRY_MAX must be >= SAGA_RETRY_BASE")
	}
	n, err := strconv.Atoi(env("SAGA_MAX_ATTEMPTS", "20"))
	if err != nil || n < 1 || n > 1000 {
		return c, errors.New("invalid SAGA_MAX_ATTEMPTS")
	}
	c.MaxAttempts = n
	c.CancelBeforePreparing, err = strconv.ParseBool(env("SAGA_CANCEL_BEFORE_PREPARING", "false"))
	if err != nil {
		return c, errors.New("invalid SAGA_CANCEL_BEFORE_PREPARING")
	}
	return c, nil
}
func RunSagaWorker(ctx context.Context, c ConsumerConfig, policy application.SagaConfig, logger *slog.Logger) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("saga database initialization failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = pool.Exec(check, `SELECT s.dispatch_deadline,s.order_id,s.next_attempt_at,o.stream FROM order_sagas s LEFT JOIN outbox_events o ON false LIMIT 0`)
	cancel()
	if err != nil {
		return errors.New("saga schema check failed")
	}
	ready := func(ctx context.Context) error {
		_, err := pool.Exec(ctx, `SELECT order_id FROM order_sagas LIMIT 0`)
		return err
	}
	server := &http.Server{Addr: env("SAGA_HTTP_ADDR", ":8089"), Handler: httpadapter.NewOutboxHealthHandler(ready), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 16}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	run, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 2)
	go func() { done <- serve(run, server, listener, 10*time.Second, logger) }()
	go func() {
		done <- application.NewSagaWorker(postgres.NewOrders(pool, application.NewFulfillment(policy)), policy.PollInterval, logger).Run(run)
	}()
	first := <-done
	stop()
	return errors.Join(first, <-done)
}
