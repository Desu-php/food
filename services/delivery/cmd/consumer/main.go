package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"example.com/food/delivery/internal/bootstrap"
)

func main() {
	cfg, err := bootstrap.LoadConsumerConfig()
	if err != nil {
		slog.Error("invalid consumer configuration")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if bootstrap.RunConsumer(ctx, cfg, logger) != nil {
		logger.Error("consumer stopped; offset retained; check migrations, event ordering and dependencies")
		os.Exit(1)
	}
}
