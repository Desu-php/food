package bootstrap

import "testing"

func TestPushConfigFailsClosed(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test@localhost/test")
	t.Setenv("APP_ENV", "test")
	t.Setenv("PUSH_PROVIDER", "fake")
	t.Setenv("PUSH_RELEVANCE_KEY", "test-only-private-relevance-key-32bytes")
	if _, err := LoadPushConfig(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ key, value string }{{"APP_ENV", "production"}, {"PUSH_LEASE", "20s"}, {"PUSH_MAX_ATTEMPTS", "0"}, {"PUSH_SEND_INTERVAL", "0s"}, {"PUSH_PROVIDER", "unknown"}, {"PUSH_RELEVANCE_KEY", "short"}, {"DELIVERY_INTERNAL_URL", "file:///tmp/secret"}} {
		t.Run(tt.key, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := LoadPushConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
