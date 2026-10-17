package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "example.com/food/delivery/internal/adapters/http"
	"example.com/food/delivery/internal/adapters/kafka"
	"example.com/food/delivery/internal/adapters/postgres"
	"example.com/food/delivery/internal/application"
)

const consumerSchemaCheck = `SELECT p.event_id,c.message_id,r.command_version,s.preparation_started FROM processed_events p LEFT JOIN command_inbox c ON false LEFT JOIN delivery_requests r ON false LEFT JOIN order_streams s ON false LIMIT 0`

func RunConsumer(ctx context.Context, c ConsumerConfig, logger *slog.Logger) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = c.OperationTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("initialize delivery fulfillment database pool failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, c.OperationTimeout)
	_, err = pool.Exec(check, consumerSchemaCheck)
	cancel()
	if err != nil {
		return errors.New("delivery fulfillment startup check failed: check connectivity and migrations")
	}
	repo := postgres.NewRepository(pool)
	maxPoll := time.Minute
	source, err := kafka.NewConsumer(c.Brokers, c.Topic, c.Group, maxPoll, c.CommandTopic)
	if err != nil {
		return err
	}
	defer source.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	ready := func(ctx context.Context) error {
		if _, err := pool.Exec(ctx, consumerSchemaCheck); err != nil {
			return err
		}
		return source.Ping(ctx)
	}
	server := &http.Server{Addr: c.HTTPAddr, Handler: httpadapter.NewOutboxHealthHandler(ready), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
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
	go func() {
		done <- application.NewConsumer(source, repo, logger).WithTopics(c.Topic, c.CommandTopic).Run(runCtx)
	}()
	logger.Info("delivery fulfillment started", "topic", c.Topic, "consumer_group", c.Group, "address", listener.Addr().String())
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
	logger.Info("delivery fulfillment stopped")
	// Do not include database error strings in process logs.
	if first != nil {
		return errors.New("fulfillment consumer stopped: inspect structured processing logs and database connectivity")
	}
	return err
}
