package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/Desu-php/food/services/orders/internal/adapters/http"
	"github.com/Desu-php/food/services/orders/internal/adapters/kafka"
	"github.com/Desu-php/food/services/orders/internal/adapters/postgres"
	"github.com/Desu-php/food/services/orders/internal/application"
)

const consumerSchemaCheck = `SELECT o.latitude,o.longitude,s.dispatch_deadline,i.event_id,s.order_id,s.delivery_version,o.courier_id FROM delivery_inbox i LEFT JOIN order_sagas s ON false LEFT JOIN orders o ON false LIMIT 0`

func RunConsumer(ctx context.Context, c ConsumerConfig, logger *slog.Logger, replay ...ReplayOptions) error {
	cfg, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = c.OperationTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("initialize orders fulfillment database pool failed")
	}
	defer pool.Close()
	check, cancel := context.WithTimeout(ctx, c.OperationTimeout)
	_, err = pool.Exec(check, consumerSchemaCheck+`; SELECT id FROM consumer_quarantine LIMIT 0`)
	cancel()
	if err != nil {
		return errors.New("orders fulfillment startup check failed: check connectivity and migrations")
	}
	policy, err := LoadSagaConfig()
	if err != nil {
		return err
	}
	repo := postgres.NewOrders(pool, application.NewFulfillment(policy))

	consumerStore := postgres.NewConsumerInbox(pool, logger)
	handler := application.NewConsumer(nil, repo, logger).Handle
	if len(replay) > 0 && replay[0].ID != 0 {
		op, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		o := replay[0]
		if err := consumerStore.Replay(op, o.ID, o.Operator, o.Note, o.Body, o.Key, handler); err != nil {
			return errors.New("consumer replay failed: inspect structured logs and replay audit")
		}
		return nil
	}
	maxPoll := 2 * time.Minute
	source, err := kafka.NewConsumer(c.Brokers, c.Topic, c.Group, maxPoll)
	if err != nil {
		return err
	}
	defer source.Close()
	process := func(ctx context.Context, m application.Message) error {
		if err := consumerStore.Process(ctx, m, handler); err != nil {
			return err
		}
		commit, cancel := context.WithTimeout(ctx, c.OperationTimeout)
		defer cancel()
		err := source.Commit(commit, m)
		if err != nil {
			consumerStore.LogCommitFailure(m)
		}
		return err
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	ready := func(ctx context.Context) error {
		if err := consumerStore.Ready(ctx); err != nil {
			return err
		}
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
	go func() { done <- application.NewConsumer(source, repo, logger).WithProcessor(process).Run(runCtx) }()
	logger.Info("orders fulfillment started", "topic", c.Topic, "consumer_group", c.Group, "address", listener.Addr().String())
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
	logger.Info("orders fulfillment stopped")
	// Do not include database error strings in process logs.
	if first != nil {
		return errors.New("fulfillment consumer stopped: inspect structured processing logs and database connectivity")
	}
	return err
}

type ReplayOptions struct {
	ID             int64
	Operator, Note string
	Body, Key      []byte
}
