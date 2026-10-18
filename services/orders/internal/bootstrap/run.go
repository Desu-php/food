package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/orders/internal/adapters/catalog"
	httpadapter "github.com/Desu-php/food/services/orders/internal/adapters/http"
	"github.com/Desu-php/food/services/orders/internal/adapters/postgres"
	"github.com/Desu-php/food/services/orders/internal/application"
)

const schemaCheck = "SELECT o.total_minor,o.pricing_snapshot,o.delivery_latitude,o.delivery_longitude,o.latitude,o.longitude,s.dispatch_deadline,o.id,o.version,o.customer_id,o.restaurant_id,o.status,o.courier_id,o.cancellation_requested,o.payment_method,o.payment_status,o.currency,o.subtotal_minor,o.created_at,o.updated_at,i.id,i.order_id,i.menu_item_id,i.name,i.quantity,i.unit_price_minor,i.total_price_minor,k.customer_id,k.key,k.request_hash,k.order_id,k.response,e.event_id,e.claim_token,e.lease_until,e.stream,s.order_id,s.next_attempt_at FROM orders o LEFT JOIN order_items i ON i.order_id=o.id LEFT JOIN idempotency_keys k ON k.order_id=o.id LEFT JOIN outbox_events e ON e.aggregate_id=o.id LEFT JOIN order_sagas s ON s.order_id=o.id LIMIT 0"

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

	client, err := catalog.NewClient(cfg.CatalogBaseURL, cfg.CatalogTimeout)
	if err != nil {
		return err
	}
	defer client.Close()
	fulfillment := application.NewFulfillment(cfg.Saga)
	repo := postgres.NewOrders(pool, fulfillment)
	if !cfg.DevAuthEnabled {
		cfg.RestaurantGrants = auth.RestaurantGrants()
	}
	orders := application.NewOrders(repo, client, fulfillment)
	ready := func(ctx context.Context) error { _, err := pool.Exec(ctx, schemaCheck); return err }
	if cfg.DevAuthEnabled {
		logger.Warn("development identity enabled; do not expose this API to untrusted clients")
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           auth.Wrap(httpadapter.NewHandler(orders, ready, logger, cfg.RequestTimeout, cfg.DevAuthEnabled, application.NewRestaurants(repo, cfg.RestaurantGrants, fulfillment))),
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
	logger.Info("orders started", "address", listener.Addr().String())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("orders shutting down")
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
