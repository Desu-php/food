package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DispatcherIDs     []int64
	AppEnv            string
	DevAuthEnabled    bool
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
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8085"), DatabaseURL: os.Getenv("DATABASE_URL")}
	c.AppEnv = env("APP_ENV", "production")
	if c.AppEnv != "production" && c.AppEnv != "development" && c.AppEnv != "test" {
		return c, errors.New("APP_ENV must be production, development or test")
	}
	enabled, err := strconv.ParseBool(env("DEV_AUTH_ENABLED", "false"))
	if err != nil {
		return c, errors.New("invalid DEV_AUTH_ENABLED")
	}
	c.DevAuthEnabled = enabled
	c.DispatcherIDs = []int64{}
	if raw := os.Getenv("DEV_DISPATCHER_IDS"); raw != "" {
		for _, v := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				return c, errors.New("invalid DEV_DISPATCHER_IDS")
			}
			c.DispatcherIDs = append(c.DispatcherIDs, id)
		}
	}

	if enabled && c.AppEnv != "development" && c.AppEnv != "test" {
		return c, errors.New("DEV_AUTH_ENABLED is forbidden outside development/test")
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
