package bootstrap

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"example.com/food/orders/internal/application"
)

type Config struct {
	Saga              application.SagaConfig
	RestaurantGrants  map[int64][]int64
	CatalogBaseURL    string
	CatalogTimeout    time.Duration
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
	c := Config{HTTPAddr: env("HTTP_ADDR", ":8081"), DatabaseURL: os.Getenv("DATABASE_URL")}
	c.CatalogBaseURL = env("CATALOG_BASE_URL", "http://localhost:8082")
	c.AppEnv = env("APP_ENV", "production")
	if c.AppEnv != "production" && c.AppEnv != "development" && c.AppEnv != "test" {
		return c, errors.New("APP_ENV must be production, development or test")
	}
	enabled, err := strconv.ParseBool(env("DEV_AUTH_ENABLED", "false"))
	if err != nil {
		return c, errors.New("invalid DEV_AUTH_ENABLED")
	}
	c.DevAuthEnabled = enabled
	c.RestaurantGrants = make(map[int64][]int64)
	if raw := os.Getenv("DEV_RESTAURANT_GRANTS"); raw != "" {
		for _, grant := range strings.Split(raw, ",") {
			parts := strings.Split(grant, ":")
			if len(parts) != 2 {
				return c, errors.New("invalid DEV_RESTAURANT_GRANTS")
			}
			user, e1 := strconv.ParseInt(parts[0], 10, 64)
			restaurant, e2 := strconv.ParseInt(parts[1], 10, 64)
			if e1 != nil || e2 != nil || user <= 0 || restaurant <= 0 {
				return c, errors.New("invalid DEV_RESTAURANT_GRANTS")
			}
			c.RestaurantGrants[user] = append(c.RestaurantGrants[user], restaurant)
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
		{"CATALOG_TIMEOUT", "2s", &c.CatalogTimeout},
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
	if c.CatalogTimeout >= c.RequestTimeout {
		return c, errors.New("CATALOG_TIMEOUT must be less than REQUEST_TIMEOUT")
	}
	if c.WriteTimeout <= c.RequestTimeout {
		return c, errors.New("HTTP_WRITE_TIMEOUT must exceed REQUEST_TIMEOUT")
	}
	policy, err := LoadSagaConfig()
	if err != nil {
		return c, err
	}
	c.Saga = policy
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
