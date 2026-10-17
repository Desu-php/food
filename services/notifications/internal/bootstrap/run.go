package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	httpadapter "example.com/food/notifications/internal/adapters/http"
	"example.com/food/notifications/internal/adapters/kafka"
	"example.com/food/notifications/internal/adapters/postgres"
	"example.com/food/notifications/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schemaCheck = `SELECT p.event_id,n.id,f.id FROM processed_events p LEFT JOIN notifications n ON n.event_id=p.event_id LEFT JOIN failed_messages f ON false LIMIT 0`

func Run(ctx context.Context, c Config, logger *slog.Logger, replay int64) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = c.Consumer.OperationTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("initialize notifications database pool failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, c.Consumer.OperationTimeout)
	_, err = pool.Exec(check, schemaCheck)
	cancel()
	if err != nil {
		return errors.New("notifications startup check failed: check connectivity and migrations")
	}
	repo := postgres.NewInbox(pool)
	if replay != 0 {
		opctx, cancel := context.WithTimeout(ctx, c.Consumer.OperationTimeout)
		defer cancel()
		m, err := repo.FailedMessage(opctx, replay)
		if err != nil {
			return err
		}
		e, duplicate, err := application.NewNotifications(repo).Handle(opctx, m)
		if err != nil {
			return err
		}
		if err := repo.Resolve(opctx, replay); err != nil {
			return err
		}
		logger.Info("quarantined event replayed", "failure_id", replay, "event_id", e.EventID, "correlation_id", e.CorrelationID, "duplicate", duplicate)
		return nil
	}
	// Budget includes every bounded DB operation and backoff, plus quarantine/commit.
	maxPoll := time.Duration(c.Consumer.MaxAttempts)*(c.Consumer.OperationTimeout+c.Consumer.RetryMax) + 2*c.Consumer.OperationTimeout + time.Minute
	source, err := kafka.NewConsumer(c.Brokers, c.Topic, c.Group, maxPoll)
	if err != nil {
		return err
	}
	defer source.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	ready := func(ctx context.Context) error {
		if _, err := pool.Exec(ctx, schemaCheck); err != nil {
			return err
		}
		return source.Ping(ctx)
	}
	server := &http.Server{Addr: c.HTTPAddr, Handler: httpadapter.NewHandler(ready), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	listener, err := net.Listen("tcp", c.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	done := make(chan error, 2)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	go func() { done <- application.NewConsumer(source, repo, c.Consumer, logger).Run(runCtx) }()
	logger.Info("notifications started", "topic", c.Topic, "consumer_group", c.Group, "address", listener.Addr().String())
	var first error
	completed := 0
	select {
	case <-ctx.Done():
	case first = <-done:
		completed = 1
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	err = server.Shutdown(shutdown)
	if err != nil {
		_ = server.Close()
	}
	for completed < 2 {
		first = errors.Join(first, <-done)
		completed++
	}
	logger.Info("notifications stopped")
	// Do not include database error strings in process logs.
	if first != nil {
		return errors.New("notification consumer stopped: inspect structured processing logs and database connectivity")
	}
	return err
}
