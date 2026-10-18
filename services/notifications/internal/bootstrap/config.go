package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/application"
)

type Config struct {
	DatabaseURL     string
	HTTPAddr        string
	Brokers         []string
	Topic           string
	Group           string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
	Consumer        application.ConsumerConfig
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func LoadConfig() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: env("HTTP_ADDR", ":8083"), Brokers: strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ","), Topic: env("ORDERS_EVENTS_TOPIC", events.Topic), Group: env("KAFKA_CONSUMER_GROUP", "notifications-v1")}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	for i, b := range c.Brokers {
		c.Brokers[i] = strings.TrimSpace(b)
		if c.Brokers[i] == "" {
			return c, errors.New("invalid KAFKA_BROKERS")
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO"))); err != nil {
		return c, errors.New("invalid LOG_LEVEL")
	}
	for _, v := range []struct {
		key, fallback string
		target        *time.Duration
	}{
		{"DB_OPERATION_TIMEOUT", "5s", &c.Consumer.OperationTimeout}, {"CONSUMER_RETRY_BASE", "1s", &c.Consumer.RetryBase}, {"CONSUMER_RETRY_MAX", "10s", &c.Consumer.RetryMax}, {"SHUTDOWN_TIMEOUT", "10s", &c.ShutdownTimeout},
	} {
		d, err := time.ParseDuration(env(v.key, v.fallback))
		if err != nil || d < time.Millisecond || d > time.Hour {
			return c, fmt.Errorf("invalid %s: expected duration between 1ms and 1h", v.key)
		}
		*v.target = d
	}
	n, err := strconv.Atoi(env("CONSUMER_MAX_ATTEMPTS", "5"))
	if err != nil || n < 1 || n > 10 {
		return c, errors.New("invalid CONSUMER_MAX_ATTEMPTS")
	}
	c.Consumer.MaxAttempts = n
	if c.Consumer.RetryMax < c.Consumer.RetryBase {
		return c, errors.New("CONSUMER_RETRY_MAX must be >= CONSUMER_RETRY_BASE")
	}
	return c, nil
}
