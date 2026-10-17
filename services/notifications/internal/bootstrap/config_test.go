package bootstrap

import "testing"

func TestConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")
	if c, err := LoadConfig(); err != nil || c.Topic != "orders.events.v1" || c.Group != "notifications-v1" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, tt := range []struct{ key, value string }{{"DATABASE_URL", ""}, {"CONSUMER_MAX_ATTEMPTS", "0"}, {"CONSUMER_MAX_ATTEMPTS", "11"}, {"DB_OPERATION_TIMEOUT", "0s"}, {"CONSUMER_RETRY_MAX", "1ms"}, {"KAFKA_BROKERS", ","}} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
