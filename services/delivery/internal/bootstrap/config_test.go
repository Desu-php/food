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
