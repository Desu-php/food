package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Desu-php/food/services/notifications/internal/bootstrap"
)

func main() {
	replay := flag.Int64("replay-failed", 0, "reprocess one quarantined message ID and exit")
	parked := flag.Int64("replay-parked", 0, "replay a consumer_quarantine ID and exit")
	operator := flag.String("operator", "", "operator identity for replay audit")
	note := flag.String("resolution-note", "", "reason and reconciliation evidence")
	recovery := flag.String("recovery-file", "", "optional corrected contract JSON")
	key := flag.String("recovery-key", "", "optional corrected Kafka key")
	flag.Parse()
	options := bootstrap.ReplayOptions{ID: *parked, Operator: *operator, Note: *note}
	if *recovery != "" {
		body, err := os.ReadFile(*recovery)
		if err != nil || len(body) > 64<<10 {
			slog.Error("cannot read bounded recovery file")
			os.Exit(1)
		}
		options.Body = body
	}
	if *key != "" {
		options.Key = []byte(*key)
	}
	if err := run(*replay, options); err != nil {
		slog.Error("notifications stopped", "error", err)
		os.Exit(1)
	}
}

func run(replay int64, options bootstrap.ReplayOptions) error {
	c, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: c.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return bootstrap.Run(ctx, c, logger, replay, options)
}
