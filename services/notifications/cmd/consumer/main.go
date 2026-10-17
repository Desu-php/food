package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"example.com/food/notifications/internal/bootstrap"
)

func main() {
	replay := flag.Int64("replay-failed", 0, "reprocess one quarantined message ID and exit")
	flag.Parse()
	if err := run(*replay); err != nil {
		slog.Error("notifications stopped", "error", err)
		os.Exit(1)
	}
}

func run(replay int64) error {
	c, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bootstrap.Run(ctx, c, logger, replay)
}
