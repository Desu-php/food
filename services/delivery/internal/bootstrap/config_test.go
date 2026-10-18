package bootstrap

import "testing"

func TestDevelopmentIdentityForbiddenInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("DEV_AUTH_ENABLED", "true")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("dev identity allowed in production")
	}
	t.Setenv("APP_ENV", "test")
	t.Setenv("DEV_DISPATCHER_IDS", "20,21")
	c, err := LoadConfig()
	if err != nil || len(c.DispatcherIDs) != 2 {
		t.Fatal(c, err)
	}
	t.Setenv("DEV_DISPATCHER_IDS", "20,invalid")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("invalid allowlist accepted")
	}
}

func TestDispatchConfigBounds(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("APP_ENV", "test")
	t.Setenv("DEV_AUTH_ENABLED", "true")
	for _, tc := range []struct{ key, value string }{
		{"DISPATCH_RADIUS_METERS", "NaN"}, {"DISPATCH_RADIUS_METERS", "0"}, {"DISPATCH_RADIUS_METERS", "100001"},
		{"DISPATCH_MAX_ATTEMPTS", "0"}, {"DISPATCH_LEASE", "2s"}, {"LOCATION_TTL", "2s"},
		{"LOCATION_MIN_INTERVAL", "0s"}, {"DISPATCH_ENABLED", "invalid"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
