package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	"github.com/Desu-php/food/services/delivery/internal/adapters/postgres"
	redisadapter "github.com/Desu-php/food/services/delivery/internal/adapters/redis"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

const schemaCheck = "SELECT d.delivery_latitude,d.delivery_longitude,r.customer_id,o.expires_at,d.latitude,d.longitude,d.dispatch_deadline,d.dispatch_attempts,d.id,d.order_id,d.version,d.order_version,d.ready,c.id,c.updated_at,d.preparation_started,p.delivery_id,p.id,e.event_id,e.stream,i.event_id FROM deliveries d LEFT JOIN delivery_requests r ON r.order_id=d.order_id LEFT JOIN couriers c ON c.id=d.courier_id LEFT JOIN payment_confirmations p ON p.delivery_id=d.id LEFT JOIN outbox_events e ON e.aggregate_id=d.id LEFT JOIN processed_events i ON false LEFT JOIN courier_offers o ON o.delivery_id=d.id LIMIT 0"

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	cfg.DevAuthEnabled = cfg.Authentication.DevEnabled
	auth, err := security.New(cfg.Authentication)
	if err != nil {
		return err
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	poolConfig.MaxConns = cfg.DBMaxConns
	poolConfig.MinConns = cfg.DBMinConns
	poolConfig.MaxConnLifetime = cfg.DBMaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.DBMaxConnIdleTime
	poolConfig.ConnConfig.ConnectTimeout = cfg.DBConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("initialize database pool failed")
	}
	defer pool.Close()
	startupCtx, cancel := context.WithTimeout(ctx, cfg.DBConnectTimeout)
	err = pool.Ping(startupCtx)
	if err == nil {
		_, err = pool.Exec(startupCtx, schemaCheck)
	}
	cancel()
	if err != nil {
		return errors.New("database startup check failed: check connectivity and migrations")
	}

	if !cfg.DevAuthEnabled {
		cfg.DispatcherIDs = auth.Dispatchers()
	}
	repo := postgres.NewRepository(pool)
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return errors.New("invalid REDIS_URL")
	}
	redisOptions.DialTimeout = time.Second
	redisOptions.ReadTimeout = time.Second
	redisOptions.WriteTimeout = time.Second
	redisOptions.ContextTimeoutEnabled = true
	redisOptions.MaxRetries = -1
	locationsClient := redis.NewClient(redisOptions)
	defer locationsClient.Close()
	locations := redisadapter.NewLocations(locationsClient, "{delivery-dispatch}", cfg.LocationTTL, cfg.LocationInterval)
	dispatch := application.NewDispatch(repo, locations, cfg.Dispatch)
	offers := application.NewOffers(repo)
	tracking := application.NewTracking(repo, locations, cfg.LocationTTL, cfg.LocationMaxAccuracy)
	service := application.NewDelivery(repo, cfg.DispatcherIDs).WithDispatch(dispatch).WithOffers(offers).WithTracking(tracking)
	workerCtx, stopWorker := context.WithCancel(ctx)
	expiryDone := make(chan struct{})
	go func() { defer close(expiryDone); offers.RunExpiry(workerCtx, logger) }()
	workerDone := make(chan struct{})
	if cfg.DispatchEnabled {
		go func() { defer close(workerDone); dispatch.Run(workerCtx, logger) }()
	} else {
		close(workerDone)
	}
	defer func() { stopWorker(); <-workerDone; <-expiryDone }()

	ready := func(ctx context.Context) error { _, err := pool.Exec(ctx, schemaCheck); return err }
	if cfg.DevAuthEnabled {
		logger.Warn("development identity enabled; do not expose this API to untrusted clients")
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpadapter.PushRelevance(auth.Wrap(httpadapter.NewHandler(service, ready, logger, cfg.RequestTimeout, cfg.DevAuthEnabled, cfg.Tracking)), repo, os.Getenv("PUSH_RELEVANCE_KEY")),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	return serve(ctx, server, listener, cfg.ShutdownTimeout, logger)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration, logger *slog.Logger) error {
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("delivery started", "address", listener.Addr().String())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("delivery shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-done
			return err
		}
		err := <-done
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
