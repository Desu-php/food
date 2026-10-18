package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/orders/internal/bootstrap"
)

func main() {
	cfg, err := bootstrap.LoadConsumerConfig()
	if err != nil {
		slog.Error("invalid saga database configuration")
		os.Exit(1)
	}
	policy, err := bootstrap.LoadSagaConfig()
	if err != nil {
		slog.Error("invalid saga policy")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if bootstrap.RunSagaWorker(ctx, cfg, policy, logger) != nil {
		logger.Error("saga worker stopped")
		os.Exit(1)
	}
}
