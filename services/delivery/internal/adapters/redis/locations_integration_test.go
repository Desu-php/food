package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/Desu-php/food/contracts/geo"
	ids "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

func locationIndex(t *testing.T) (*Locations, *redis.Client) {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL for Redis GEO integration tests")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.MaxRetries = -1
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	key := "test:" + ids.NewID()
	t.Cleanup(func() { client.Del(context.Background(), key+":geo", key+":seen") })
	return NewLocations(client, key, time.Minute, 5*time.Second), client
}
func TestNearestFreshnessRadiusTieAndRateLimit(t *testing.T) {
	index, client := locationIndex(t)
	ctx := context.Background()
	p := geo.Point{Latitude: 38.56, Longitude: 68.77}
	// Reversed insertion order must not influence equal-distance selection.
	for _, id := range []int64{9, 2} {
		if err := index.Update(ctx, id, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := index.Update(ctx, 2, p); !errors.Is(err, application.ErrLocationRate) {
		t.Fatal(err)
	}
	far := geo.Point{Latitude: 39, Longitude: 69}
	if err := index.Update(ctx, 1, far); err != nil {
		t.Fatal(err)
	}
	near := geo.Point{Latitude: 38.561, Longitude: 68.77}
	if err := index.Update(ctx, 3, near); err != nil {
		t.Fatal(err)
	}
	if err := index.Update(ctx, 4, p); err != nil {
		t.Fatal(err)
	}
	// Expire one member while the shared index remains live.
	if err := client.ZAdd(ctx, index.key+":seen", redis.Z{Score: float64(time.Now().Add(-2 * time.Minute).UnixMilli()), Member: "4"}).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := index.Nearest(ctx, p, 1000)
	if err != nil || len(got) != 3 {
		t.Fatal(got, err)
	}
	for i, id := range []int64{2, 9, 3} {
		if got[i].CourierID != id || !got[i].ExpiresAt.After(time.Now()) {
			t.Fatal(got)
		}
	}
	if n, err := client.ZCard(ctx, index.key+":geo").Result(); err != nil || n != 4 {
		t.Fatal("expired GEO member retained", n, err)
	}
	got, err = index.Nearest(ctx, geo.Point{Latitude: 0, Longitude: 0}, 1000)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
func TestRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: time.Millisecond * 50})
	defer client.Close()
	index := NewLocations(client, "test", time.Minute, time.Second)
	if err := index.Update(context.Background(), 1, geo.Point{}); !errors.Is(err, application.ErrLocationUnavailable) {
		t.Fatal(err)
	}
	if _, err := index.Nearest(context.Background(), geo.Point{}, 1000); !errors.Is(err, application.ErrLocationUnavailable) {
		t.Fatal(err)
	}
}
