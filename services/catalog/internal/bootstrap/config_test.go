package bootstrap

import (
	"log/slog"
	"testing"
	"time"
)

func configEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"INTERNAL_HTTP_ADDR", "HTTP_ADDR", "LOG_LEVEL", "DB_MAX_CONNS", "DB_MIN_CONNS", "DB_CONNECT_TIMEOUT", "DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "REQUEST_TIMEOUT", "SHUTDOWN_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost/catalog_test")
}

func TestConfigDefaults(t *testing.T) {
	configEnv(t)
	cfg, err := LoadConfig()
	if err != nil || cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 10 || cfg.RequestTimeout != 5*time.Second || cfg.WriteTimeout <= cfg.RequestTimeout {
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

func TestInternalListenerMustDiffer(t *testing.T) {
	configEnv(t)
	t.Setenv("INTERNAL_HTTP_ADDR", ":8080")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("internal listener shares public address")
	}
}
