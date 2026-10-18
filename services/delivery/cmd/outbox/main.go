package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/delivery/internal/bootstrap"
)

func main() {
	requeue := flag.String("requeue", "", "requeue one DEAD event UUID and exit")
	flag.Parse()
	if err := run(*requeue); err != nil {
		slog.Error("outbox stopped", "error", err)
		os.Exit(1)
	}
}

func run(requeue string) error {
	cfg, err := bootstrap.LoadWorkerConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bootstrap.RunWorker(ctx, cfg, logger, requeue)
}
