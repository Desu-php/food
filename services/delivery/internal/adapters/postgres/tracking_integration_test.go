package postgres

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	ids "github.com/Desu-php/food/contracts/orders/v1"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	redisadapter "github.com/Desu-php/food/services/delivery/internal/adapters/redis"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
	redis "github.com/redis/go-redis/v9"
)

func bindTracking(t *testing.T, r *Repository, order, version int64) {
	t.Helper()
	c := createCommand(order, version, commands.CreateDelivery)
	c.Payload.CustomerID = 7
	c.Payload.FulfillmentType = "DELIVERY"
	c.Payload.DeliveryAddress = "Customer address"
	if err := r.ProcessCommand(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestTrackingOwnershipPersistenceLegacyAndConflict(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	bindTracking(t, r, 42, 1)
	s, err := r.TrackingState(ctx, 7, 42)
	if err != nil || s.Status != domain.PendingAssignment {
		t.Fatal(s, err)
	}
	if _, err = r.TrackingState(ctx, 8, 42); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = NewRepository(pool).TrackingState(ctx, 7, 42); err != nil {
		t.Fatal("ownership lost after repository restart", err)
	}
	c := createCommand(42, 2, commands.CreateDelivery)
	c.Payload.CustomerID = 8
	c.Payload.FulfillmentType = "DELIVERY"
	c.Payload.DeliveryAddress = "Customer address"
	if err = r.ProcessCommand(ctx, c); !errors.Is(err, application.ErrEventConflict) {
		t.Fatal("owner changed", err)
	}
	if err = r.ProcessCommand(ctx, createCommand(43, 1, commands.CreateDelivery)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.TrackingState(ctx, 7, 43); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("historical owner invented", err)
	}
	for i, kind := range []string{ids.OrderCreated, ids.OrderAccepted, ids.OrderPreparing, ids.OrderReadyForPickup, ids.OrderPickedUpByCustomer} {
		e := orderEvent(44, int64(i+1), kind)
		e.Payload.FulfillmentType = "PICKUP"
		if err = r.ProcessOrder(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.TrackingState(ctx, 7, 44); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("pickup tracking exposed", err)
	}
}

func TestTrackingHTTPRedisPostgresLifecycle(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL for tracking integration")
	}
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opts.MaxRetries = -1
	client := redis.NewClient(opts)
	defer client.Close()
	key := "{tracking-test-" + ids.NewID() + "}"
	locations := redisadapter.NewLocations(client, key, time.Minute, time.Millisecond)
	defer client.Del(ctx, key+":geo", key+":seen", key+":recorded", key+":location:1")
	service := application.NewDelivery(r, []int64{20}).WithTracking(application.NewTracking(r, locations, time.Minute, 100))
	bindTracking(t, r, 42, 1)
	var deliveryID int64
	if err = pool.QueryRow(ctx, `SELECT id FROM deliveries WHERE order_id=42`).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	c := makeCourier(t, service)
	if _, err = service.Assign(ctx, 20, deliveryID, c.ID); err != nil {
		t.Fatal(err)
	}
	cfg := httpadapter.DefaultTrackingConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.Heartbeat = 20 * time.Millisecond
	h := httpadapter.NewHandler(service, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, true, cfg)
	server := httptest.NewServer(h)
	defer server.Close()
	stream := func() (*http.Response, *bufio.Reader) {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+"/orders/42/tracking", nil)
		req.Header.Set("X-Dev-Customer-ID", "7")
		res, e := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if e != nil {
			t.Fatal(e)
		}
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
		return res, bufio.NewReader(res.Body)
	}
	read := func(reader *bufio.Reader, event string) string {
		t.Helper()
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				t.Fatal(e)
			}
			if strings.TrimSpace(line) == "event: "+event {
				data, e := reader.ReadString('\n')
				if e != nil {
					t.Fatal(e)
				}
				return data
			}
		}
	}
	res, reader := stream()
	defer res.Body.Close()
	read(reader, "tracking_pending")
	// Domain preparation and pickup are real PostgreSQL operations, not SQL fixtures.
	for _, e := range []ids.Event{orderEvent(42, 2, ids.OrderAccepted), orderEvent(42, 3, ids.OrderPreparing), orderEvent(42, 4, ids.OrderReadyForPickup)} {
		if err = r.ProcessOrder(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.Pickup(ctx, c.ID, deliveryID); err != nil {
		t.Fatal(err)
	}
	read(reader, "location_unavailable")
	gps := func(latitude float64) {
		t.Helper()
		body := fmt.Sprintf(`{"latitude":%f,"longitude":69.62,"accuracy_meters":12,"recorded_at":%q}`, latitude, time.Now().UTC().Format(time.RFC3339Nano))
		req, _ := http.NewRequest("POST", server.URL+"/couriers/me/location", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dev-Courier-ID", fmt.Sprint(c.ID))
		response, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		if response.StatusCode != 204 {
			raw, _ := io.ReadAll(response.Body)
			t.Fatal(response.Status, string(raw))
		}
	}
	gps(40.28)
	if data := read(reader, "location"); !strings.Contains(data, "40.28") {
		t.Fatal(data)
	}
	gps(40.29)
	if data := read(reader, "location"); !strings.Contains(data, "40.29") {
		t.Fatal(data)
	}
	res.Body.Close()
	res2, reader2 := stream()
	defer res2.Body.Close()
	if data := read(reader2, "location"); !strings.Contains(data, "40.29") {
		t.Fatal("reconnect lost latest snapshot", data)
	}
	if _, err = service.Complete(ctx, c.ID, deliveryID, true, "tracking-test-pos"); err != nil {
		t.Fatal(err)
	}
	read(reader2, "tracking_ended")
	if data, err := io.ReadAll(reader2); err != nil || strings.Contains(string(data), "latitude") {
		t.Fatal(string(data), err)
	}
	// GPS may remain in Redis for dispatch, but terminal orders cannot read it.
	update, err := service.Track(ctx, 7, 42)
	if err != nil || update.Event != "tracking_ended" || update.Location != nil {
		t.Fatal(update, err)
	}
}
