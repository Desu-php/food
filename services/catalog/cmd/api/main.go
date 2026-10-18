package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/catalog/internal/bootstrap"
)

func main() {
	if err := run(); err != nil {
		slog.Error("catalog stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bootstrap.Run(ctx, cfg, logger)
}
