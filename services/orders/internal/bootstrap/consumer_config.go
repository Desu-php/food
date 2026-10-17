package bootstrap

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"
)

type ConsumerConfig struct {
	DatabaseURL, HTTPAddr, Topic, Group string
	Brokers                             []string
	LogLevel                            slog.Level
	OperationTimeout, ShutdownTimeout   time.Duration
}

func LoadConsumerConfig() (ConsumerConfig, error) {
	c := ConsumerConfig{DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: env("CONSUMER_HTTP_ADDR", ":8087"), Topic: env("DELIVERY_EVENTS_TOPIC", "delivery.events.v1"), Group: env("FULFILLMENT_CONSUMER_GROUP", "orders-delivery-v1"), Brokers: strings.Split(env("KAFKA_BROKERS", "localhost:19092"), ","), OperationTimeout: 5 * time.Second, ShutdownTimeout: 10 * time.Second}
	if c.DatabaseURL == "" || c.Topic == "" || c.Group == "" {
		return c, errors.New("database, topic and group required")
	}
	for i, b := range c.Brokers {
		c.Brokers[i] = strings.TrimSpace(b)
		if c.Brokers[i] == "" {
			return c, errors.New("invalid brokers")
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO"))); err != nil {
		return c, errors.New("invalid LOG_LEVEL")
	}
	return c, nil
}
