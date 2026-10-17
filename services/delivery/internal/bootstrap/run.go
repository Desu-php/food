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
	"example.com/food/delivery/internal/adapters/postgres"
	"example.com/food/delivery/internal/application"
	"example.com/food/platform/security"
)

const schemaCheck = "SELECT d.id,d.order_id,d.version,d.order_version,d.ready,c.id,c.updated_at,d.preparation_started,p.delivery_id,p.id,e.event_id,i.event_id FROM deliveries d LEFT JOIN couriers c ON c.id=d.courier_id LEFT JOIN payment_confirmations p ON p.delivery_id=d.id LEFT JOIN outbox_events e ON e.aggregate_id=d.id LEFT JOIN processed_events i ON false LIMIT 0"

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
	service := application.NewDelivery(postgres.NewRepository(pool), cfg.DispatcherIDs)
	ready := func(ctx context.Context) error { _, err := pool.Exec(ctx, schemaCheck); return err }
	if cfg.DevAuthEnabled {
		logger.Warn("development identity enabled; do not expose this API to untrusted clients")
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           auth.Wrap(httpadapter.NewHandler(service, ready, logger, cfg.RequestTimeout, cfg.DevAuthEnabled)),
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
