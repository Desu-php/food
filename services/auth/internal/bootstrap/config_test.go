package bootstrap

import (
	"testing"
)

func configEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{"APP_ENV": "production", "DATABASE_URL": "postgres://auth@localhost/auth", "AUTH_ISSUER": "https://auth.example.test", "AUTH_NOTIFICATIONS_URL": "https://notifications.internal", "AUTH_COOKIE_SECURE": "true", "AUTH_ALLOW_UNVERIFIED_LOGIN": "false", "CORS_ALLOWED_ORIGINS": "https://food.example.test", "AUTH_CSRF_KEY": "01234567890123456789012345678901", "AUTH_SERVICE_KEY": "01234567890123456789012345678901", "AUTH_CSRF_KEY_FILE": "", "AUTH_SERVICE_KEY_FILE": "", "AUTH_SIGNING_KEYS_FILE": "/run/auth/keys.json", "AUTH_ACTIVE_KID": "initial", "AUTH_ARGON_MEMORY_KIB": "19456", "AUTH_ARGON_ITERATIONS": "2", "AUTH_ARGON_PARALLELISM": "1", "AUTH_HASH_CONCURRENCY": "2", "AUTH_TLS_CERT_FILE": "", "AUTH_TLS_KEY_FILE": ""} {
		t.Setenv(k, v)
	}
}
func TestProductionCannotEnableInsecureAuthOptions(t *testing.T) {
	for _, tc := range []struct{ key, value string }{{"AUTH_COOKIE_SECURE", "false"}, {"AUTH_ALLOW_UNVERIFIED_LOGIN", "true"}, {"AUTH_ISSUER", "http://auth.example.test"}, {"AUTH_NOTIFICATIONS_URL", "http://notifications.internal"}, {"AUTH_ARGON_PARALLELISM", "256"}, {"AUTH_TLS_CERT_FILE", "/only-one-tls-file"}} {
		t.Run(tc.key, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe production config accepted")
			}
		})
	}
	configEnv(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}
