package bootstrap

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	httpadapter "github.com/Desu-php/food/services/auth/internal/adapters/http"
	"github.com/Desu-php/food/services/auth/internal/application"
)

type Config struct {
	Environment, DatabaseURL, HTTPAddr, InternalAddr, Issuer, Audience, ActiveKID, KeyFile, TLSCert, TLSKey string
	AccessTTL                                                                                               time.Duration
	Application                                                                                             application.Config
	HTTP                                                                                                    httpadapter.Config
	ArgonMemory, ArgonIterations                                                                            uint32
	ArgonParallel                                                                                           uint8
	HashConcurrency                                                                                         int
	NotificationsURL                                                                                        string
}

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func secret(key string) (string, error) {
	if path := os.Getenv(key + "_FILE"); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("cannot read " + key + "_FILE")
		}
		return strings.TrimSpace(string(body)), nil
	}
	return os.Getenv(key), nil
}
func Load() (Config, error) {
	c := Config{Environment: env("APP_ENV", "production"), DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: env("HTTP_ADDR", ":8084"), InternalAddr: env("AUTH_INTERNAL_HTTP_ADDR", "127.0.0.1:8086"), Issuer: os.Getenv("AUTH_ISSUER"), Audience: env("AUTH_AUDIENCE", "food-api"), ActiveKID: os.Getenv("AUTH_ACTIVE_KID"), KeyFile: os.Getenv("AUTH_SIGNING_KEYS_FILE"), TLSCert: os.Getenv("AUTH_TLS_CERT_FILE"), TLSKey: os.Getenv("AUTH_TLS_KEY_FILE")}
	c.NotificationsURL = env("AUTH_NOTIFICATIONS_URL", "http://localhost:8083")
	if c.Environment != "production" && c.Environment != "development" && c.Environment != "test" {
		return c, errors.New("invalid APP_ENV")
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.Environment != "production" && u.Scheme == "http")) {
		return c, errors.New("AUTH_ISSUER must be trusted HTTPS URL")
	}
	if c.DatabaseURL == "" || c.HTTPAddr == c.InternalAddr {
		return c, errors.New("database and separate internal listener required")
	}
	n, err := url.Parse(c.NotificationsURL)
	if err != nil || n.Host == "" || n.User != nil || (n.Scheme != "https" && !(c.Environment != "production" && n.Scheme == "http")) {
		return c, errors.New("trusted Notifications URL required; production requires HTTPS")
	}
	c.HTTP.Origins = strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",")
	for i := range c.HTTP.Origins {
		c.HTTP.Origins[i] = strings.TrimSpace(c.HTTP.Origins[i])
	}
	c.HTTP.CSRFKey, err = secret("AUTH_CSRF_KEY")
	if err != nil {
		return c, err
	}
	c.HTTP.ServiceKey, err = secret("AUTH_SERVICE_KEY")
	if err != nil {
		return c, err
	}
	c.HTTP.SecureCookies = true
	if s := os.Getenv("AUTH_COOKIE_SECURE"); s != "" {
		c.HTTP.SecureCookies, err = strconv.ParseBool(s)
		if err != nil {
			return c, errors.New("invalid AUTH_COOKIE_SECURE")
		}
	}
	c.Application.AllowUnverified, err = strconv.ParseBool(env("AUTH_ALLOW_UNVERIFIED_LOGIN", "false"))
	if err != nil {
		return c, errors.New("invalid AUTH_ALLOW_UNVERIFIED_LOGIN")
	}
	if c.Environment == "production" && (!c.HTTP.SecureCookies || c.Application.AllowUnverified) {
		return c, errors.New("insecure authentication options forbidden in production")
	}
	for _, item := range []struct {
		key, def string
		target   *time.Duration
	}{{"AUTH_ACCESS_TTL", "3m", &c.AccessTTL}, {"AUTH_SESSION_TTL", "720h", &c.Application.SessionTTL}, {"REQUEST_TIMEOUT", "5s", &c.HTTP.Timeout}} {
		*item.target, err = time.ParseDuration(env(item.key, item.def))
		if err != nil || *item.target <= 0 {
			return c, errors.New("invalid " + item.key)
		}
	}
	for _, item := range []struct {
		key, def string
		set      func(uint64)
	}{{"AUTH_ARGON_MEMORY_KIB", "19456", func(n uint64) { c.ArgonMemory = uint32(n) }}, {"AUTH_ARGON_ITERATIONS", "2", func(n uint64) { c.ArgonIterations = uint32(n) }}, {"AUTH_ARGON_PARALLELISM", "1", func(n uint64) { c.ArgonParallel = uint8(n) }}, {"AUTH_HASH_CONCURRENCY", "2", func(n uint64) { c.HashConcurrency = int(n) }}} {
		n, e := strconv.ParseUint(env(item.key, item.def), 10, 32)
		if e != nil || n > 262144 {
			return c, errors.New("invalid " + item.key)
		}
		if item.key == "AUTH_ARGON_PARALLELISM" && n > 8 {
			return c, errors.New("invalid " + item.key)
		}
		item.set(n)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, errors.New("both TLS files required")
	}
	return c, nil
}
