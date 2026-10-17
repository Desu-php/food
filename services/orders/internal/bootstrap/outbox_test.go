package bootstrap

import (
	"testing"
	"time"
)

func TestWorkerConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")
	c, err := LoadWorkerConfig()
	if err != nil || c.Worker.Lease != 30*time.Second || c.Topic != "orders.events.v1" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, tt := range []struct{ key, value string }{{"OUTBOX_LEASE", "10s"}, {"OUTBOX_MAX_ATTEMPTS", "0"}, {"OUTBOX_POLL_INTERVAL", "0s"}, {"OUTBOX_RETRY_MAX", "1ms"}, {"KAFKA_BROKERS", ","}} {
		t.Run(tt.key, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := LoadWorkerConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
