package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Desu-php/food/contracts/correlation"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/orders/internal/application"
)

func TestOutboxAtomicCreationCancellationAndIdempotency(t *testing.T) {
	pool := integrationPool(t)
	repo := NewOrders(pool)
	ctx := correlation.WithID(context.Background(), "atomic-request")
	o, err := repo.Create(ctx, "key", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := repo.Create(ctx, "key", strings.Repeat("a", 64), newOrder(t)); err != nil {
			t.Fatal(err)
		}
	}
	var body []byte
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate creation event: %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT body FROM outbox_events`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	e, err := events.Decode(body)
	if err != nil || e.AggregateID != o.Snapshot().ID || e.CorrelationID != "atomic-request" || e.AggregateVersion != 1 {
		t.Fatalf("event %+v %v", e, err)
	}
	if _, err := application.NewOrders(repo, nil).CancelOrder(ctx, 7, o.Snapshot().ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cancel event: %d %v", count, err)
	}
	// Rejecting the event must roll back business data as well.
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_events CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, "blocked", strings.Repeat("a", 64), newOrder(t)); err == nil {
		t.Fatal("event rejection did not abort create")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial aggregate: %d %v", count, err)
	}
}

func TestOutboxCancellationRollback(t *testing.T) {
	pool := integrationPool(t)
	repo := NewOrders(pool)
	ctx := context.Background()
	o, err := repo.Create(ctx, "key", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_cancellation CHECK(event_type<>'OrderCancelled')`); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewOrders(repo, nil).CancelOrder(ctx, 7, o.Snapshot().ID); err == nil {
		t.Fatal("accepted cancellation without event")
	}
	got, err := repo.Get(ctx, 7, o.Snapshot().ID)
	if err != nil || got.Snapshot().Version != 1 {
		t.Fatalf("partial cancellation %+v %v", got.Snapshot(), err)
	}
}

func TestOutboxConcurrentClaimsRecoveryAndFencing(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	o, err := NewOrders(pool).Create(ctx, "key", strings.Repeat("a", 64), newOrder(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewOrders(NewOrders(pool), nil).CancelOrder(ctx, 7, o.Snapshot().ID); err != nil {
		t.Fatal(err)
	}
	r := NewOutbox(pool)
	var wg sync.WaitGroup
	claims := make(chan application.ClaimedEvent, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, found, err := NewOutbox(pool).Claim(ctx, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if found {
				claims <- e
			}
		}()
	}
	wg.Wait()
	close(claims)
	var first application.ClaimedEvent
	count := 0
	for e := range claims {
		first = e
		count++
	}
	if count != 1 || first.Version != 1 {
		t.Fatalf("simultaneous active claims: %d %+v", count, first)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET lease_until=now()-interval '1 second' WHERE event_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	recovered, found, err := r.Claim(ctx, time.Minute)
	if err != nil || !found || !recovered.Recovered || recovered.Token == first.Token || recovered.ID != first.ID {
		t.Fatalf("recovery: %+v %v %v", recovered, found, err)
	}
	if err := r.Published(ctx, first); !errors.Is(err, application.ErrLeaseLost) {
		t.Fatalf("stale worker acknowledged: %v", err)
	}
	if err := r.Failed(ctx, recovered, time.Millisecond, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := r.Claim(ctx, time.Minute); err != nil || found {
		t.Fatalf("dead predecessor bypassed: %v %v", found, err)
	}
	if err := r.Requeue(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	e, found, err := r.Claim(ctx, time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := r.Published(ctx, e); err != nil {
		t.Fatal(err)
	}
	next, found, err := r.Claim(ctx, time.Minute)
	if err != nil || !found || next.Version != 2 {
		t.Fatalf("next version %+v %v %v", next, found, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET published_at=now()-interval '10 days' WHERE status='PUBLISHED'`); err != nil {
		t.Fatal(err)
	}
	deleted, err := r.Prune(ctx, 7*24*time.Hour)
	if err != nil || deleted != 1 {
		t.Fatalf("retention %d %v", deleted, err)
	}
	if err := r.Published(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxMigrationRoundTrip(t *testing.T) {
	pool := integrationPool(t, "000001_orders", "000002_outbox")
	ctx := context.Background()
	for _, suffix := range []string{"down", "up"} {
		body, err := os.ReadFile("../../../migrations/000002_outbox." + suffix + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"000003_fulfillment", "000004_saga", "000005_pickup", "000007_dispatch_snapshot"} {
		body, err := os.ReadFile("../../../migrations/" + name + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewOrders(pool).Create(ctx, "after-up", strings.Repeat("a", 64), newOrder(t)); err != nil {
		t.Fatal(err)
	}
}
