package bootstrap

import (
	"log/slog"
	"testing"
	"time"
)

func configEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CATALOG_TIMEOUT", "CATALOG_BASE_URL", "APP_ENV", "DEV_AUTH_ENABLED", "HTTP_ADDR", "LOG_LEVEL", "DB_MAX_CONNS", "DB_MIN_CONNS", "DB_CONNECT_TIMEOUT", "DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "REQUEST_TIMEOUT", "SHUTDOWN_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/orders_test")
}

func TestConfigDefaults(t *testing.T) {
	configEnv(t)
	cfg, err := LoadConfig()
	if err != nil || cfg.HTTPAddr != ":8081" || cfg.DBMaxConns != 10 || cfg.RequestTimeout != 5*time.Second || cfg.WriteTimeout <= cfg.RequestTimeout {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	t.Setenv("LOG_LEVEL", "DEBUG")
	cfg, err = LoadConfig()
	if err != nil || cfg.LogLevel != slog.LevelDebug {
		t.Fatal("log level not parsed")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, tt := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"LOG_LEVEL", "bad"}, {"DB_MAX_CONNS", "0"}, {"DB_MIN_CONNS", "11"},
		{"DB_MAX_CONNS", "abc"}, {"REQUEST_TIMEOUT", "0s"}, {"HTTP_READ_TIMEOUT", "bad"}, {"HTTP_WRITE_TIMEOUT", "1s"},
	} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestDevelopmentIdentityRestriction(t *testing.T) {
	configEnv(t)
	t.Setenv("DEV_AUTH_ENABLED", "true")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("production accepted dev identity")
	}
	for _, mode := range []string{"development", "test"} {
		t.Setenv("APP_ENV", mode)
		if _, err := LoadConfig(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("APP_ENV", "staging")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
