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
	httpadapter "github.com/Desu-php/food/services/catalog/internal/adapters/http"
	"github.com/Desu-php/food/services/catalog/internal/adapters/postgres"
	"github.com/Desu-php/food/services/catalog/internal/application"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
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
		// A reachable database without migrations is not a ready catalog.
		_, err = pool.Exec(startupCtx, "SELECT r.latitude,r.longitude,r.id,r.name,r.is_open,r.created_at,r.updated_at,c.id,c.name,c.sort_order,c.created_at,c.updated_at,i.id,i.name,i.description,i.price_minor,i.currency,i.is_available,i.created_at,i.updated_at,i.deleted_at FROM restaurants r LEFT JOIN menu_categories c ON c.restaurant_id=r.id LEFT JOIN menu_items i ON i.restaurant_id=c.restaurant_id AND i.category_id=c.id LIMIT 0")
	}
	cancel()
	if err != nil {
		return errors.New("database startup check failed: check connectivity and migrations")
	}

	repo := postgres.NewRestaurants(pool)
	restaurants := application.NewRestaurants(repo)
	menu := application.NewMenu(repo, postgres.NewMenu(pool))
	ready := func(ctx context.Context) error {
		// Also verifies the schema remains available after startup.
		_, err := pool.Exec(ctx, "SELECT r.latitude,r.longitude,r.id,r.name,r.is_open,r.created_at,r.updated_at,c.id,c.name,c.sort_order,c.created_at,c.updated_at,i.id,i.name,i.description,i.price_minor,i.currency,i.is_available,i.created_at,i.updated_at,i.deleted_at FROM restaurants r LEFT JOIN menu_categories c ON c.restaurant_id=r.id LEFT JOIN menu_items i ON i.restaurant_id=c.restaurant_id AND i.category_id=c.id LIMIT 0")
		return err
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           auth.Wrap(httpadapter.NewHandler(restaurants, menu, ready, logger, cfg.RequestTimeout)),
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
	defer listener.Close()
	internalListener, err := net.Listen("tcp", cfg.InternalHTTPAddr)
	if err != nil {
		return err
	}
	defer internalListener.Close()
	internalServer := &http.Server{Handler: httpadapter.NewInternalHandler(application.NewQuotes(postgres.NewMenu(pool)), logger, cfg.RequestTimeout), ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 1 << 20}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- serve(runCtx, server, listener, cfg.ShutdownTimeout, logger) }()
	go func() { done <- serve(runCtx, internalServer, internalListener, cfg.ShutdownTimeout, logger) }()
	first := <-done
	cancel()
	second := <-done
	return errors.Join(first, second)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration, logger *slog.Logger) error {
	defer listener.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	logger.Info("catalog started", "address", listener.Addr().String())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("catalog shutting down")
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
