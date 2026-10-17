package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	InternalHTTPAddr  string
	HTTPAddr          string
	DatabaseURL       string
	LogLevel          slog.Level
	DBMaxConns        int32
	DBMinConns        int32
	DBConnectTimeout  time.Duration
	DBMaxConnLifetime time.Duration
	DBMaxConnIdleTime time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	ShutdownTimeout   time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8080"), DatabaseURL: os.Getenv("DATABASE_URL")}
	c.InternalHTTPAddr = env("INTERNAL_HTTP_ADDR", "127.0.0.1:8082")
	if c.InternalHTTPAddr == c.HTTPAddr {
		return c, errors.New("internal and public HTTP addresses must differ")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "INFO"))); err != nil {
		return c, errors.New("invalid LOG_LEVEL")
	}
	for _, item := range []struct {
		key      string
		fallback int
		target   *int32
	}{
		{"DB_MAX_CONNS", 10, &c.DBMaxConns}, {"DB_MIN_CONNS", 0, &c.DBMinConns},
	} {
		value, err := strconv.ParseInt(env(item.key, strconv.Itoa(item.fallback)), 10, 32)
		if err != nil || value < 0 {
			return c, fmt.Errorf("invalid %s", item.key)
		}
		*item.target = int32(value)
	}
	if c.DBMaxConns < 1 || c.DBMinConns > c.DBMaxConns {
		return c, errors.New("require 0 <= DB_MIN_CONNS <= DB_MAX_CONNS and DB_MAX_CONNS > 0")
	}
	for _, item := range []struct {
		key      string
		fallback string
		target   *time.Duration
	}{
		{"DB_CONNECT_TIMEOUT", "5s", &c.DBConnectTimeout},
		{"DB_MAX_CONN_LIFETIME", "1h", &c.DBMaxConnLifetime},
		{"DB_MAX_CONN_IDLE_TIME", "5m", &c.DBMaxConnIdleTime},
		{"HTTP_READ_HEADER_TIMEOUT", "5s", &c.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", "10s", &c.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", "15s", &c.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", "60s", &c.IdleTimeout},
		{"REQUEST_TIMEOUT", "5s", &c.RequestTimeout},
		{"SHUTDOWN_TIMEOUT", "10s", &c.ShutdownTimeout},
	} {
		value, err := time.ParseDuration(env(item.key, item.fallback))
		if err != nil || value <= 0 {
			return c, fmt.Errorf("invalid %s: expected positive duration", item.key)
		}
		*item.target = value
	}
	if c.WriteTimeout <= c.RequestTimeout {
		return c, errors.New("HTTP_WRITE_TIMEOUT must exceed REQUEST_TIMEOUT")
	}
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
