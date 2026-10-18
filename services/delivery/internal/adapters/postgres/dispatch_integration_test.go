package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	events "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/contracts/geo"
	orderevents "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	redisadapter "github.com/Desu-php/food/services/delivery/internal/adapters/redis"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func geoDelivery(t *testing.T, r *Repository, order int64) domain.Delivery {
	t.Helper()
	c := createCommand(order, 1, commands.CreateDelivery)
	c.Payload.RestaurantLocation = &geo.Point{Latitude: 38.56, Longitude: 68.77}
	deadline := time.Now().Add(10 * time.Minute)
	c.Payload.DispatchDeadline = &deadline
	if err := r.ProcessCommand(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	list, err := r.List(context.Background(), 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if d.OrderID == order {
			return d
		}
	}
	t.Fatal("delivery not created")
	return domain.Delivery{}
}
func candidates(ids ...int64) []application.Candidate {
	result := []application.Candidate{}
	for i, id := range ids {
		result = append(result, application.Candidate{CourierID: id, DistanceMeters: float64(i), ExpiresAt: time.Now().Add(time.Minute)})
	}
	return result
}
func TestConcurrentDispatchAndManualAssignment(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{true: "manual_vs_auto", false: "two_workers"}[manual], func(t *testing.T) {
			pool := integrationPool(t)
			r := NewRepository(pool)
			s := application.NewDelivery(r, []int64{20})
			ctx := context.Background()
			cfg := application.DefaultDispatchConfig()
			d1 := geoDelivery(t, r, 42)
			d2 := geoDelivery(t, r, 43)
			c := makeCourier(t, s)
			claim1, ok, err := r.ClaimDispatch(ctx, cfg)
			if err != nil || !ok {
				t.Fatal(claim1, ok, err)
			}
			claim2, ok, err := r.ClaimDispatch(ctx, cfg)
			if err != nil || !ok || claim1.Delivery.ID == claim2.Delivery.ID {
				t.Fatal(claim2, ok, err)
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, 2)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, e := r.AssignDispatch(ctx, claim1, candidates(c.ID), cfg.Cooldown)
				errs <- e
			}()
			go func() {
				defer wg.Done()
				<-start
				if manual {
					_, e := s.Assign(ctx, 20, d1.ID, c.ID)
					if errors.Is(e, domain.ErrConflict) {
						e = nil
					}
					errs <- e
				} else {
					_, e := r.AssignDispatch(ctx, claim2, candidates(c.ID), cfg.Cooldown)
					errs <- e
				}
			}()
			close(start)
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			var assigned, outbox int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE courier_id=$1 AND status='ASSIGNED'`, c.ID).Scan(&assigned); err != nil || assigned != 1 {
				t.Fatal(assigned, err)
			}
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='CourierAssigned'`).Scan(&outbox); err != nil || outbox != 1 {
				t.Fatal(outbox, err)
			}
			// Retrying an old lease cannot add another event or assign the second job.
			if _, err = r.AssignDispatch(ctx, claim1, candidates(c.ID), cfg.Cooldown); err != nil {
				t.Fatal(err)
			}
			_ = d2
		})
	}
}
func TestDispatchCooldownStatusDeadlineAndCancellation(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	d := geoDelivery(t, r, 42)
	c1 := makeCourier(t, s)
	c2 := makeCourier(t, s)
	c3 := makeCourier(t, s)
	if _, err := s.SetAvailability(ctx, c1.ID, c1.ID, domain.Offline); err != nil {
		t.Fatal(err)
	}
	// A genuine active assignment makes c2 BUSY.
	busy := geoDelivery(t, r, 43)
	if _, err := s.Assign(ctx, 20, busy.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := r.ClaimDispatch(ctx, cfg)
	if err != nil || !ok || claim.Delivery.ID != d.ID {
		t.Fatal(claim, err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c1.ID, c2.ID, c3.ID), cfg.Cooldown); err != nil || !assigned {
		t.Fatal(assigned, err)
	}
	if _, err := s.Decline(ctx, c3.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAvailability(ctx, c1.ID, c1.ID, domain.Available); err != nil {
		t.Fatal(err)
	}
	claim, ok, err = r.ClaimDispatch(ctx, cfg)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c3.ID, c1.ID), cfg.Cooldown); err != nil || !assigned {
		t.Fatal(assigned, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.CourierID != c1.ID {
		t.Fatal(got, err)
	}
	if err = r.ProcessCommand(ctx, func() commands.Command {
		c := createCommand(42, 2, commands.CancelDelivery)
		c.Payload.RestaurantLocation = d.RestaurantLocation
		c.Payload.DispatchDeadline = d.DispatchDeadline
		return c
	}()); err != nil {
		t.Fatal(err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c3.ID), cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	// Candidate freshness is rechecked inside PostgreSQL even after a successful Redis lookup.
	d = geoDelivery(t, r, 44)
	claim, _, err = r.ClaimDispatch(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	stale := candidates(c3.ID)
	stale[0].ExpiresAt = time.Now().Add(-time.Second)
	if assigned, err := r.AssignDispatch(ctx, claim, stale, cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE deliveries SET dispatch_deadline=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c3.ID), cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
}
func TestDispatchLeaseRecoveryAndStaleOwner(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	old, _, err := r.ClaimDispatch(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE deliveries SET dispatch_lease_until=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	current, found, err := r.ClaimDispatch(ctx, cfg)
	if err != nil || !found || current.Token == old.Token {
		t.Fatal(current, found, err)
	}
	if assigned, err := r.AssignDispatch(ctx, old, candidates(c.ID), cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
	if assigned, err := r.AssignDispatch(ctx, current, candidates(c.ID), cfg.Cooldown); err != nil || !assigned {
		t.Fatal(assigned, err)
	}
}
func TestDispatchRedisAndPostgres(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL")
	}
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	defer client.Close()
	key := "test:" + time.Now().Format("150405.000000000")
	defer client.Del(ctx, key+":geo", key+":seen")
	index := redisadapter.NewLocations(client, key, time.Minute, time.Second)
	worker := application.NewDispatch(r, index, cfg)
	d := geoDelivery(t, r, 42)
	c1 := makeCourier(t, s)
	c2 := makeCourier(t, s)
	for _, v := range []struct {
		id int64
		p  geo.Point
	}{{c1.ID, geo.Point{Latitude: 38.562, Longitude: 68.77}}, {c2.ID, *d.RestaurantLocation}} {
		if err := worker.UpdateLocation(ctx, v.id, v.p); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := worker.Step(ctx); err != nil || !found {
		t.Fatal(found, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.CourierID != c2.ID {
		t.Fatal(got, err)
	}
}
func TestAuthenticatedLocationAndDispatchRetry(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	cfg := application.DefaultDispatchConfig()
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	defer unreachable.Close()
	index := redisadapter.NewLocations(unreachable, "test", time.Minute, time.Second)
	worker := application.NewDispatch(r, index, cfg)
	s := application.NewDelivery(r, []int64{20}).WithDispatch(worker)
	c := makeCourier(t, s)
	d := geoDelivery(t, r, 42)
	h := httpadapter.NewHandler(s, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	p := security.Principal{Subject: "oidc-courier", UserID: 100, CourierID: c.ID, Roles: []string{"courier"}}
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/couriers/me/location", `{"latitude":38.56,"longitude":68.77,"courier_id":99}`, 400},
		{"/couriers/me/location", `{"latitude":91,"longitude":68.77}`, 400},
		{"/couriers/me/location", `{"longitude":68.77}`, 400},
		{"/couriers/me/location", `{"latitude":38.56,"longitude":68.77}`, 503},
		{"/deliveries/1/dispatch/retry", "", 403},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), p))
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatal(tc.path, res.Code, res.Body)
		}
	}
	// Redis outage leaves pending work and manual assignment usable.
	if worked, err := worker.Step(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	got, err := r.Get(context.Background(), 0, d.ID)
	if err != nil || got.Status != domain.PendingAssignment || got.DispatchReason != "redis_unavailable" {
		t.Fatal(got, err)
	}
	if _, err = s.Assign(context.Background(), 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	var event string
	if err = pool.QueryRow(context.Background(), `SELECT event_type FROM outbox_events WHERE aggregate_id=$1 AND aggregate_version=2`, d.ID).Scan(&event); err != nil || event != events.CourierAssigned {
		t.Fatal(event, err)
	}
}

func TestDispatchCancellationEventFenceAndHistoricalManualFallback(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	claim, _, err := r.ClaimDispatch(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []orderevents.Event{orderEvent(42, 2, orderevents.OrderAccepted), orderEvent(42, 3, orderevents.OrderCancellationRequested)} {
		if err = r.ProcessOrder(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c.ID), cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	_ = d
	// Historical deliveries never invent coordinates or acquire assignments automatically.
	historical := accepted(t, r, 43)
	worker := application.NewDispatch(r, nil, cfg)
	if _, err = worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Assign(ctx, 20, historical.ID, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchBoundedRetriesAndOperatorRestart(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	cfg.MaxAttempts = 1
	d := geoDelivery(t, r, 42)
	claim, found, err := r.ClaimDispatch(ctx, cfg)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if err = r.FinishDispatch(ctx, claim, cfg, "no_available_courier", false); err != nil {
		t.Fatal(err)
	}
	if _, found, err = r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.DispatchReason != "attempts_exhausted:no_available_courier" {
		t.Fatal(got, err)
	}
	if err = r.RetryDispatch(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err = r.ClaimDispatch(ctx, cfg); err != nil || !found {
		t.Fatal(found, err)
	}
}

func TestDispatchRespectsConsumerQuarantine(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	claim, found, err := r.ClaimDispatch(ctx, cfg)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO consumer_quarantine(topic,partition,offset_value,message_key,body,reason,attempts) VALUES('delivery.commands.v1',0,1,''::bytea,''::bytea,'unsupported_contract',1)`); err != nil {
		t.Fatal(err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c.ID), cfg.Cooldown); err != nil || assigned {
		t.Fatal(assigned, err)
	}
	if _, found, err = r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	// Replay resolves the fence under the existing exclusive lock. No domain facts are fabricated here.
	if _, err = pool.Exec(ctx, `UPDATE consumer_quarantine SET resolved_at=now()`); err != nil {
		t.Fatal(err)
	}
	if assigned, err := r.AssignDispatch(ctx, claim, candidates(c.ID), cfg.Cooldown); err != nil || !assigned {
		t.Fatal(assigned, err)
	}
}

func TestTwoWorkersRunStepsConcurrently(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	geoDelivery(t, r, 42)
	geoDelivery(t, r, 43)
	c := makeCourier(t, s)
	locations := &fixedLocations{candidates: candidates(c.ID)}
	workers := []*application.Dispatch{application.NewDispatch(r, locations, cfg), application.NewDispatch(NewRepository(pool), locations, cfg)}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for _, worker := range workers {
		go func() {
			defer wg.Done()
			<-start
			if _, err := worker.Step(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE status='ASSIGNED'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='CourierAssigned'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

type fixedLocations struct {
	application.Locations
	candidates []application.Candidate
}

func (l *fixedLocations) Nearest(context.Context, geo.Point, float64) ([]application.Candidate, error) {
	return l.candidates, nil
}

func TestDispatchStopsAfterPreparationAndManualFallbackStillWorks(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	if _, err := s.Assign(ctx, 20, d.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.ProcessOrder(ctx, orderEvent(42, 2, orderevents.OrderAccepted)); err != nil {
		t.Fatal(err)
	}
	ready(t, r, 42)
	if _, err := s.Decline(ctx, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	if err := r.RetryDispatch(ctx, d.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	other := makeCourier(t, s)
	if _, err := s.Assign(ctx, 20, d.ID, other.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManualAssignmentCompletesWhileRedisSearchWaits(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	s := application.NewDelivery(r, []int64{20})
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	d := geoDelivery(t, r, 42)
	automatic := makeCourier(t, s)
	manual := makeCourier(t, s)
	index := &waitingLocations{entered: make(chan struct{}), release: make(chan struct{}), candidates: candidates(automatic.ID)}
	worker := application.NewDispatch(r, index, cfg)
	result := make(chan error, 1)
	go func() { _, err := worker.Step(ctx); result <- err }()
	select {
	case <-index.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reach location search")
	}
	// A claim row lock must already have been committed before the slow external lookup.
	manualCtx, cancel := context.WithTimeout(ctx, time.Second)
	_, assignErr := s.Assign(manualCtx, 20, d.ID, manual.ID)
	cancel()
	close(index.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if assignErr != nil {
		t.Fatal("Redis lookup blocked manual assignment", assignErr)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || got.CourierID != manual.ID {
		t.Fatal(got, err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='CourierAssigned'`, d.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

type waitingLocations struct {
	application.Locations
	entered, release chan struct{}
	candidates       []application.Candidate
}

func (l *waitingLocations) Nearest(ctx context.Context, _ geo.Point, _ float64) ([]application.Candidate, error) {
	close(l.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.release:
		return l.candidates, nil
	}
}

func TestExpiredLastLeaseStopsAtRetryBudget(t *testing.T) {
	pool := integrationPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	cfg.MaxAttempts = 1
	d := geoDelivery(t, r, 42)
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || !found {
		t.Fatal(found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deliveries SET dispatch_lease_until=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || !got.DispatchPaused || got.DispatchAttempts != 1 || got.DispatchReason != "attempts_exhausted:lease_expired" {
		t.Fatal(got, err)
	}
}
