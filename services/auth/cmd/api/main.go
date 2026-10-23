package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/auth/internal/bootstrap"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	c, err := bootstrap.Load()
	if err != nil {
		logger.Error("auth configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = bootstrap.Run(ctx, c, logger); err != nil {
		logger.Error("auth stopped", "error_type", "startup_or_server")
		os.Exit(1)
	}
}
