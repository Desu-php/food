package redis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
	redis "github.com/redis/go-redis/v9"
)

func TestLiveLocationAtomicOrderingTTLAndSharedRateLimit(t *testing.T) {
	index, client := locationIndex(t)
	ctx := context.Background()
	t.Cleanup(func() { client.Del(ctx, index.locationKey(7), index.locationKey(8)) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	l := domain.Location{CourierID: 7, Latitude: 40.28, Longitude: 69.62, AccuracyMeters: 12, RecordedAt: now, ReceivedAt: now}
	if err := index.Store(ctx, l); err != nil {
		t.Fatal(err)
	}
	got, found, err := index.Latest(ctx, 7)
	if err != nil || !found || got.Latitude != l.Latitude || !got.RecordedAt.Equal(now) || got.ReceivedAt.IsZero() {
		t.Fatal(got, found, err)
	}
	if ttl := client.PTTL(ctx, index.locationKey(7)).Val(); ttl <= 0 || ttl > index.ttl {
		t.Fatal("payload has no bounded TTL", ttl)
	}
	if candidates, err := index.Nearest(ctx, geo.Point{Latitude: 40.28, Longitude: 69.62}, 1000); err != nil || len(candidates) != 1 || candidates[0].CourierID != 7 || candidates[0].ExpiresAt.After(now.Add(index.ttl)) {
		t.Fatal("new GPS format must remain a fresh dispatch candidate", candidates, err)
	}
	if err := index.Store(ctx, l); !errors.Is(err, domain.ErrStaleLocation) {
		t.Fatal(err)
	}
	l.RecordedAt = now.Add(-time.Second)
	l.Latitude = 41
	if err := index.Store(ctx, l); !errors.Is(err, domain.ErrStaleLocation) {
		t.Fatal(err)
	}
	l.RecordedAt = now.Add(time.Millisecond)
	if err := index.Store(ctx, l); !errors.Is(err, application.ErrLocationRate) {
		t.Fatal(err)
	}
	// Rate limit is shared with the original dispatch GPS format.
	if err := index.Update(ctx, 7, geo.Point{Latitude: 42, Longitude: 69}); !errors.Is(err, application.ErrLocationRate) {
		t.Fatal(err)
	}
	l.CourierID = 8
	l.RecordedAt = now.Add(6 * time.Second)
	if err := index.Store(ctx, l); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	// Staleness is checked by timestamp even when the Redis key still exists.
	client.HSet(ctx, index.locationKey(7), "recorded", now.Add(-2*time.Minute).UnixMicro())
	client.ZAdd(ctx, index.key+":recorded", redis.Z{Score: float64(now.Add(-2 * time.Minute).UnixMilli()), Member: "7"})
	if _, found, err = index.Latest(ctx, 7); err != nil || found {
		t.Fatal(found, err)
	}
	if candidates, err := index.Nearest(ctx, geo.Point{Latitude: 40.28, Longitude: 69.62}, 1000); err != nil || len(candidates) != 0 {
		t.Fatal("dispatch used an expired observation", candidates, err)
	}
	client.Del(ctx, index.locationKey(7), index.key+":seen")
	index.ttl = 50 * time.Millisecond
	l.CourierID = 7
	l.RecordedAt = time.Now().UTC()
	if err := index.Store(ctx, l); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, found, err = index.Latest(ctx, 7); err != nil || found {
		t.Fatal(found, err)
	}
	if client.Exists(ctx, index.locationKey(7)).Val() != 0 {
		t.Fatal("expired payload retained")
	}
}

func TestConcurrentLiveLocationUpdatesAndPolarTracking(t *testing.T) {
	index, client := locationIndex(t)
	index.interval = 0
	ctx := context.Background()
	t.Cleanup(func() { client.Del(ctx, index.locationKey(7)) })
	now := time.Now().UTC().Truncate(time.Microsecond)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := index.Store(ctx, domain.Location{CourierID: 7, Latitude: 89, Longitude: 69, AccuracyMeters: 10, RecordedAt: now.Add(time.Duration(i) * time.Microsecond), ReceivedAt: now})
			if err != nil && !errors.Is(err, domain.ErrStaleLocation) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, found, err := index.Latest(ctx, 7)
	if err != nil || !found || !got.RecordedAt.Equal(now.Add(19*time.Microsecond)) {
		t.Fatal(got, found, err)
	}
	if client.ZScore(ctx, index.key+":geo", "7").Err() != redis.Nil {
		t.Fatal("polar observation must not enter GEO")
	}
}

func TestLiveLocationRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond})
	defer client.Close()
	index := NewLocations(client, "test", time.Minute, time.Second)
	l := domain.Location{CourierID: 7, Latitude: 40, Longitude: 69, AccuracyMeters: 10, RecordedAt: time.Now()}
	if err := index.Store(context.Background(), l); !errors.Is(err, application.ErrLocationUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := index.Latest(context.Background(), 7); !errors.Is(err, application.ErrLocationUnavailable) {
		t.Fatal(err)
	}
}
