package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	offers "github.com/Desu-php/food/contracts/courieroffers/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	fulfillment "github.com/Desu-php/food/contracts/fulfillment/v1"
	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/notifications/internal/adapters/http"
	"github.com/Desu-php/food/services/notifications/internal/adapters/kafka"
	"github.com/Desu-php/food/services/notifications/internal/adapters/postgres"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schemaCheck = `SELECT p.event_id,n.id,f.id FROM processed_events p LEFT JOIN notifications n ON n.event_id=p.event_id LEFT JOIN failed_messages f ON false LIMIT 0`

func Run(ctx context.Context, c Config, logger *slog.Logger, replay int64, recovery ...ReplayOptions) error {
	authentication, err := security.Load()
	if err != nil {
		return err
	}
	auth, err := security.New(authentication)
	if err != nil {
		return err
	}
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
	_, err = pool.Exec(check, schemaCheck+`; SELECT id FROM consumer_quarantine LIMIT 0; SELECT recipient_id,expires_at FROM notifications LIMIT 0; SELECT id FROM device_registrations LIMIT 0`)
	cancel()
	if err != nil {
		return errors.New("notifications startup check failed: check connectivity and migrations")
	}
	repo := postgres.NewInbox(pool)
	if replay != 0 {
		return errors.New("legacy quarantine migrated: use -replay-parked with consumer_quarantine ID and audit options")
	}

	consumerStore := postgres.NewConsumerInbox(pool, logger).WithRetry(postgres.ConsumerRetry{Attempts: c.Consumer.MaxAttempts, Timeout: c.Consumer.OperationTimeout, Base: c.Consumer.RetryBase, Max: c.Consumer.RetryMax})
	handle := func(ctx context.Context, m application.Message) error {
		if m.Topic != c.Topic {
			if m.Topic == env("DELIVERY_EVENTS_TOPIC", delivery.Topic) {
				m.Topic = delivery.Topic
			}
			return application.NewBusinessNotifications(repo).Handle(ctx, m)
		}
		e, duplicate, err := application.NewNotifications(repo).Handle(ctx, m)
		if err == nil {
			message := "notification intent created"
			if duplicate {
				message = "duplicate event skipped"
			}
			logger.Info(message, "event_id", e.EventID, "correlation_id", e.CorrelationID, "topic", m.Topic, "partition", m.Partition, "offset", m.Offset)
		}
		return err
	}
	if len(recovery) > 0 && recovery[0].ID != 0 {
		op, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		o := recovery[0]
		if err := consumerStore.Replay(op, o.ID, o.Operator, o.Note, o.Body, o.Key, handle); err != nil {
			return errors.New("consumer replay failed: inspect structured logs and replay audit")
		}
		return nil
	}
	// Budget includes every bounded DB operation and backoff, plus quarantine/commit.
	maxPoll := time.Duration(c.Consumer.MaxAttempts)*(c.Consumer.OperationTimeout+c.Consumer.RetryMax) + 2*c.Consumer.OperationTimeout + time.Minute
	source, err := kafka.NewConsumer(c.Brokers, c.Topic, c.Group, maxPoll, env("DELIVERY_EVENTS_TOPIC", delivery.Topic), offers.Topic, fulfillment.Topic)
	if err != nil {
		return err
	}
	defer source.Close()
	process := func(ctx context.Context, m application.Message) error {
		if err := consumerStore.Process(ctx, m, handle); err != nil {
			return err
		}
		commit, cancel := context.WithTimeout(ctx, c.Consumer.OperationTimeout)
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
		if _, err := pool.Exec(ctx, schemaCheck); err != nil {
			return err
		}
		return source.Ping(ctx)
	}
	server := &http.Server{Addr: c.HTTPAddr, Handler: auth.CORS(auth.Wrap(httpadapter.DeviceHandler(application.NewDevices(postgres.NewPushStore(pool)), httpadapter.NewHandler(ready), postgres.NewPushStore(pool).Metrics, authentication.DevEnabled)), "/v1/devices"), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	if authentication.AuthServiceURL != "" {
		server.Handler = httpadapter.AuthRevocations(server.Handler, authentication.AuthServiceKey, postgres.NewPushStore(pool).RevokeUserDevices)
	}
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
		done <- application.NewConsumer(source, repo, c.Consumer, logger).WithProcessor(process).Run(runCtx)
	}()
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

type ReplayOptions struct {
	ID             int64
	Operator, Note string
	Body, Key      []byte
}
