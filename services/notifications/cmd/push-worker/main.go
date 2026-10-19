package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/notifications/internal/bootstrap"
)

func main() {
	cfg, err := bootstrap.LoadPushConfig()
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
		err = bootstrap.RunPush(ctx, cfg, logger)
	}
	if err != nil {
		slog.Error("push worker stopped", "error", err)
		os.Exit(1)
	}
}
