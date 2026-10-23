package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	httpadapter "github.com/Desu-php/food/services/auth/internal/adapters/http"
	"github.com/Desu-php/food/services/auth/internal/adapters/postgres"
	"github.com/Desu-php/food/services/auth/internal/adapters/tokens"
	"github.com/Desu-php/food/services/auth/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, c Config, logger *slog.Logger) error {
	passwords, err := tokens.NewPasswords(c.ArgonMemory, c.ArgonIterations, c.ArgonParallel, c.HashConcurrency)
	if err != nil {
		return err
	}
	jwt, err := tokens.NewJWT(c.Issuer, c.Audience, c.ActiveKID, c.KeyFile, c.AccessTTL)
	if err != nil {
		return err
	}
	pc, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	pc.MaxConns = 10
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.MaxConnLifetime = time.Hour
	pc.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return errors.New("initialize auth database failed")
	}
	defer pool.Close()
	repo := postgres.New(pool)
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = repo.Ready(startup)
	cancel()
	if err != nil {
		return errors.New("auth database startup failed: check connectivity and migrations")
	}
	svc, err := application.New(repo, passwords, jwt, c.Application)
	if err != nil {
		return err
	}
	handler, err := httpadapter.New(svc, c.HTTP, logger, repo.Ready)
	if err != nil {
		return err
	}
	public, err := net.Listen("tcp", c.HTTPAddr)
	if err != nil {
		return err
	}
	defer public.Close()
	internal, err := net.Listen("tcp", c.InternalAddr)
	if err != nil {
		return err
	}
	defer internal.Close()
	server := func(h http.Handler) *http.Server {
		return &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	}
	a, b := server(handler.Public(jwt.PublicKeys())), server(handler.Internal())
	run, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 2)
	revoked := make(chan struct{})
	go func() { defer close(revoked); runRevocations(run, repo, c.NotificationsURL, c.HTTP.ServiceKey, logger) }()
	go func() { done <- serve(run, a, public, c.TLSCert, c.TLSKey) }()
	go func() { done <- serve(run, b, internal, c.TLSCert, c.TLSKey) }()
	pruned := make(chan struct{})
	go func() {
		defer close(pruned)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				op, cancel := context.WithTimeout(run, 5*time.Second)
				err := repo.Prune(op)
				cancel()
				if err != nil {
					logger.Error("auth cleanup failed", "error_type", "database")
				}
			}
		}
	}()
	logger.Info("auth started", "address", public.Addr().String())
	first := <-done
	stop()
	second := <-done
	<-pruned
	<-revoked
	return errors.Join(first, second)
}
func serve(ctx context.Context, s *http.Server, l net.Listener, cert, key string) error {
	done := make(chan error, 1)
	go func() {
		if cert != "" {
			done <- s.ServeTLS(l, cert, key)
		} else {
			done <- s.Serve(l)
		}
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		op, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(op); err != nil {
			s.Close()
			<-done
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
