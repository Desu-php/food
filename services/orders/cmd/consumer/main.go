package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"example.com/food/orders/internal/bootstrap"
)

func main() {
	replay := flag.Int64("replay-failed", 0, "replay a parked message and exit")
	operator := flag.String("operator", "", "operator identity for replay audit")
	note := flag.String("resolution-note", "", "reason and reconciliation evidence")
	recovery := flag.String("recovery-file", "", "optional corrected contract JSON; original bytes are retained")
	key := flag.String("recovery-key", "", "optional corrected Kafka key")
	flag.Parse()
	options := bootstrap.ReplayOptions{ID: *replay, Operator: *operator, Note: *note}
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

	cfg, err := bootstrap.LoadConsumerConfig()
	if err != nil {
		slog.Error("invalid consumer configuration")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if bootstrap.RunConsumer(ctx, cfg, logger, options) != nil {
		logger.Error("consumer stopped; offset retained; check migrations, event ordering and dependencies")
		os.Exit(1)
	}
}
