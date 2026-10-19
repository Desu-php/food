package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	offers "github.com/Desu-php/food/contracts/courieroffers/v1"
	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/application"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

func pushConfig() application.PushConfig {
	return application.PushConfig{Lease: time.Second, Timeout: 100 * time.Millisecond, DBTimeout: time.Second, PollInterval: time.Millisecond, RetryBase: time.Millisecond, RetryMax: time.Millisecond, SendInterval: time.Millisecond, MaxAttempts: 3, StaleAfter: 720 * time.Hour, CleanupInterval: time.Hour}
}
func register(t *testing.T, r *PushStore, user, owner int64, kind, target string) domain.DeviceRegistration {
	t.Helper()
	d, err := application.NewDevices(r).Register(context.Background(), domain.DeviceRegistration{OwnerType: kind, OwnerID: owner, RegisteredBy: user, Platform: "ANDROID", InstallationID: target})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func orderPush(t *testing.T, r *Inbox, kind string) {
	t.Helper()
	m := message(t, events.OrderCreated, 42)
	var e events.Event
	if err := json.Unmarshal(m.Body, &e); err != nil {
		t.Fatal(err)
	}
	e.EventType = kind
	if kind != events.OrderCreated {
		e.AggregateVersion = 2
	}
	if kind == events.OrderReadyForPickup {
		e.Payload.FulfillmentType = "PICKUP"
	}
	var err error
	m.Body, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = application.NewNotifications(r).Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := application.NewNotifications(r).Handle(context.Background(), m); err != nil || !duplicate {
		t.Fatal(duplicate, err)
	}
}

type resultProvider struct {
	calls  atomic.Int32
	result domain.ProviderResult
	after  func()
}

func (p *resultProvider) Send(_ context.Context, _ string, _ domain.PushMessage) domain.ProviderResult {
	p.calls.Add(1)
	if p.after != nil {
		p.after()
	}
	return p.result
}

type liveOffer struct {
	live bool
	err  error
}

func (l liveOffer) Live(context.Context, int64, int64) (bool, error) { return l.live, l.err }
func worker(r *PushStore, p application.PushProvider, live bool, log *bytes.Buffer) *application.PushWorker {
	return application.NewPushWorker(r, p, liveOffer{live: live}, pushConfig(), slog.New(slog.NewJSONHandler(log, nil)))
}
func TestDeviceRegistrationTransferAndCleanup(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	r := NewPushStore(pool)
	one := register(t, r, 8, 7, domain.Customer, "private-installation-one")
	repeat := register(t, r, 8, 7, domain.Customer, "private-installation-one")
	if one.ID != repeat.ID {
		t.Fatal("repeat duplicated")
	}
	register(t, r, 8, 7, domain.Customer, "private-installation-two")
	list, err := r.Devices(ctx, 8)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	newOwner := register(t, r, 9, 10, domain.Customer, "private-installation-one")
	if newOwner.ID == one.ID {
		t.Fatal("ownership reused revocable identifier")
	}
	if err = r.Deactivate(ctx, 8, newOwner.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old user revoked new registration", err)
	}
	if err = r.Deactivate(ctx, 8, one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE device_registrations SET last_seen_at=now()-interval '60 days' WHERE id=$1`, newOwner.ID); err != nil {
		t.Fatal(err)
	}
	if err = r.PruneDevices(ctx, 720*time.Hour); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err = pool.QueryRow(ctx, `SELECT is_active FROM device_registrations WHERE id=$1`, newOwner.ID).Scan(&active); err != nil || active {
		t.Fatal(active, err)
	}
}
func TestPushFanoutConcurrentWorkersAndNoTokenLogs(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	register(t, r, 8, 7, domain.Customer, "private-installation-two")
	orderPush(t, NewInbox(pool), events.OrderReadyForPickup)
	p := &resultProvider{result: domain.ProviderResult{Code: "accepted", MessageID: "fake-id"}}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var log bytes.Buffer
			w := worker(r, p, true, &log)
			for range 8 {
				if _, err := w.Step(ctx); err != nil {
					t.Error(err)
				}
				time.Sleep(2 * time.Millisecond)
			}
			if strings.Contains(log.String(), "private-installation") {
				t.Error("identifier leaked to logs")
			}
		}()
	}
	wg.Wait()
	if p.calls.Load() != 2 {
		t.Fatal("duplicate or missing sends", p.calls.Load())
	}
	var status string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT status FROM notifications`).Scan(&status); err != nil || status != domain.Sent {
		t.Fatal(status, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_attempts`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
}
func TestPushTemporaryPermanentAndUnregistered(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result domain.ProviderResult
		active bool
		status string
	}{
		{"temporary", domain.ProviderResult{Code: "unavailable", Temporary: true}, true, domain.Failed},
		{"payload_invalid", domain.ProviderResult{Code: "invalid_argument"}, true, domain.Failed},
		{"unregistered", domain.ProviderResult{Code: "unregistered", InvalidDevice: true}, false, domain.Failed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := integrationPool(t)
			r := NewPushStore(pool)
			register(t, r, 8, 7, domain.Customer, "private-installation-one")
			orderPush(t, NewInbox(pool), events.OrderDelivered)
			p := &resultProvider{result: tt.result}
			var log bytes.Buffer
			w := worker(r, p, true, &log)
			for range 5 {
				if _, err := w.Step(context.Background()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(3 * time.Millisecond)
			}
			var status string
			var active bool
			if err := pool.QueryRow(context.Background(), `SELECT status FROM notifications`).Scan(&status); err != nil || status != tt.status {
				t.Fatal(status, err)
			}
			if err := pool.QueryRow(context.Background(), `SELECT is_active FROM device_registrations`).Scan(&active); err != nil || active != tt.active {
				t.Fatal(active, err)
			}
			expected := int32(1)
			if tt.result.Temporary {
				expected = 3
			}
			if p.calls.Load() != expected {
				t.Fatal(p.calls.Load())
			}
		})
	}
}
func TestPushCrashRecoveryFencesOldWorkerAndMayDuplicate(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	orderPush(t, NewInbox(pool), events.OrderAccepted)
	if _, err := r.Expand(ctx); err != nil {
		t.Fatal(err)
	}
	d, ok, err := r.ClaimPush(ctx, pushConfig())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	p := &resultProvider{result: domain.ProviderResult{Code: "accepted"}}
	p.Send(ctx, "private-installation-one", d.Message)
	// Process dies after provider acceptance. The committed attempt stays UNKNOWN.
	if _, err = pool.Exec(ctx, `UPDATE push_deliveries SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	restart := worker(NewPushStore(pool), p, true, &log)
	if _, err = restart.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatal("ambiguous send was not retried")
	}
	if err = r.FinishPush(ctx, d, domain.ProviderResult{Code: "accepted"}, time.Millisecond, 3); !errors.Is(err, domain.ErrLeaseLost) {
		t.Fatal("old worker was not fenced", err)
	}
	var unknown int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_attempts WHERE code='UNKNOWN'`).Scan(&unknown); err != nil || unknown != 1 {
		t.Fatal(unknown, err)
	}
}
func offerMessage(t *testing.T, kind, status string) application.Message {
	t.Helper()
	e := offers.Event{EventID: events.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: 2, AggregateVersion: 2, CorrelationID: "offer-correlation", OccurredAt: time.Now(), Payload: offers.Payload{OfferID: 1, DeliveryID: 2, OrderID: 42, CourierID: 77, Status: status, ExpiresAt: time.Now().Add(30 * time.Second)}}
	if kind == offers.CourierOfferClosed {
		e.AggregateVersion = 3
	}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return application.Message{Topic: offers.Topic, Key: []byte("2"), Body: body}
}
func TestOfferCancellationReorderingAndRelevance(t *testing.T) {
	for _, scenario := range []string{"closed_first", "closed_after", "expired", "remote_closed", "remote_unavailable", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			pool := integrationPool(t)
			r := NewPushStore(pool)
			inbox := NewInbox(pool)
			ctx := context.Background()
			d := register(t, r, 9, 77, domain.Courier, "private-courier-installation")
			created := offerMessage(t, offers.CourierOfferCreated, "PENDING")
			closed := offerMessage(t, offers.CourierOfferClosed, "CANCELLED")
			if scenario == "closed_first" {
				if err := application.NewBusinessNotifications(inbox).Handle(ctx, closed); err != nil {
					t.Fatal(err)
				}
			}
			if err := application.NewBusinessNotifications(inbox).Handle(ctx, created); err != nil {
				t.Fatal(err)
			}
			if err := application.NewBusinessNotifications(inbox).Handle(ctx, created); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Expand(ctx); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "closed_after":
				if err := application.NewBusinessNotifications(inbox).Handle(ctx, closed); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := pool.Exec(ctx, `UPDATE notifications SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := r.Deactivate(ctx, 9, d.ID); err != nil {
					t.Fatal(err)
				}
			}
			p := &resultProvider{result: domain.ProviderResult{Code: "accepted"}}
			var log bytes.Buffer
			relevance := liveOffer{live: scenario != "remote_closed"}
			if scenario == "remote_unavailable" {
				relevance = liveOffer{err: errors.New("unavailable")}
			}
			w := application.NewPushWorker(r, p, relevance, pushConfig(), slog.New(slog.NewJSONHandler(&log, nil)))
			// Closed-offer notifications are factual and may still be delivered; assert
			// specifically that no obsolete offer-created attempt reaches the provider.
			if scenario == "closed_first" || scenario == "closed_after" {
				if _, err := pool.Exec(ctx, `UPDATE notifications SET status='CANCELLED' WHERE kind='CourierOfferClosed'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if p.calls.Load() != 0 {
				t.Fatal("obsolete offer sent")
			}
			if scenario == "remote_unavailable" {
				var status string
				if err := pool.QueryRow(ctx, `SELECT status FROM push_deliveries`).Scan(&status); err != nil || status != domain.Pending {
					t.Fatal(status, err)
				}
			}
		})
	}
}
func TestTemporaryFailureRecoversAndNewDevicesDoNotReplay(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	orderPush(t, NewInbox(pool), events.OrderDelivered)
	p := &resultProvider{result: domain.ProviderResult{Code: "unavailable", Temporary: true}}
	var log bytes.Buffer
	w := worker(r, p, true, &log)
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	register(t, r, 8, 7, domain.Customer, "private-installation-two")
	time.Sleep(3 * time.Millisecond)
	p.result = domain.ProviderResult{Code: "accepted"}
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	var status string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM notifications`).Scan(&status); err != nil || status != domain.Sent {
		t.Fatal(status, err)
	}
}
func TestDeviceTransferCancelsQueuedDelivery(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	orderPush(t, NewInbox(pool), events.OrderAccepted)
	if _, err := r.Expand(ctx); err != nil {
		t.Fatal(err)
	}
	register(t, r, 9, 10, domain.Customer, "private-installation-one")
	p := &resultProvider{result: domain.ProviderResult{Code: "accepted"}}
	var log bytes.Buffer
	if _, err := worker(r, p, true, &log).Step(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("old user's delivery sent after transfer")
	}
}
func TestPushQuarantinePausesWithoutDropping(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	orderPush(t, NewInbox(pool), events.OrderAccepted)
	if _, err := pool.Exec(ctx, `INSERT INTO consumer_quarantine(topic,partition,offset_value,message_key,body,reason,attempts) VALUES('test',0,1,'','', 'invalid_contract',1)`); err != nil {
		t.Fatal(err)
	}
	p := &resultProvider{result: domain.ProviderResult{Code: "accepted"}}
	var log bytes.Buffer
	w := worker(r, p, true, &log)
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("fence bypassed")
	}
	if _, err := pool.Exec(ctx, `UPDATE consumer_quarantine SET resolved_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("notification dropped behind fence")
	}
}
func TestConcurrentRegistrationHasOneActiveOwner(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	var wg sync.WaitGroup
	for user := int64(1); user <= 12; user++ {
		wg.Add(1)
		go func(user int64) {
			defer wg.Done()
			_, err := application.NewDevices(r).Register(context.Background(), domain.DeviceRegistration{OwnerType: domain.Customer, OwnerID: user, RegisteredBy: user, Platform: "IOS", InstallationID: "private-contended-installation"})
			if err != nil {
				t.Error(err)
			}
		}(user)
	}
	wg.Wait()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM device_registrations WHERE is_active`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestPushMigrationDoesNotSendHistoricalIntents(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	down, err := os.ReadFile("../../../migrations/000006_push.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	m := message(t, events.OrderCreated, 42)
	var e events.Event
	if err = json.Unmarshal(m.Body, &e); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	if _, err = pool.Exec(ctx, `INSERT INTO processed_events(event_id,order_id,aggregate_version,fingerprint) VALUES($1,42,1,$2)`, e.EventID, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notifications(event_id,order_id,aggregate_version,customer_id,kind,correlation_id) VALUES($1,42,1,7,'OrderCreated','legacy')`, e.EventID); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../../migrations/000006_push.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(up)); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM notifications`).Scan(&status); err != nil || status != domain.Cancelled {
		t.Fatal("historical push backfilled", status, err)
	}
	// Old insert shape still works between migration and consumer deployment.
	next := events.NewID()
	if _, err = pool.Exec(ctx, `INSERT INTO processed_events(event_id,order_id,aggregate_version,fingerprint) VALUES($1,43,1,$2)`, next, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notifications(event_id,order_id,aggregate_version,customer_id,kind,correlation_id) VALUES($1,43,1,7,'OrderCreated','legacy')`, next); err != nil {
		t.Fatal("old producer incompatible during rollout", err)
	}
}
func TestOrderTerminalFactCancelsUnsentIntent(t *testing.T) {
	pool := integrationPool(t)
	r := NewPushStore(pool)
	ctx := context.Background()
	register(t, r, 8, 7, domain.Customer, "private-installation-one")
	inbox := NewInbox(pool)
	orderPush(t, inbox, events.OrderAccepted)
	if _, err := r.Expand(ctx); err != nil {
		t.Fatal(err)
	}
	m := message(t, events.OrderCancelled, 42)
	var e events.Event
	if err := json.Unmarshal(m.Body, &e); err != nil {
		t.Fatal(err)
	}
	e.AggregateVersion = 3
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	m.Body = body
	if _, _, err = application.NewNotifications(inbox).Handle(ctx, m); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = pool.QueryRow(ctx, `SELECT status FROM notifications WHERE kind='OrderAccepted'`).Scan(&status); err != nil || status != domain.Cancelled {
		t.Fatal(status, err)
	}
	d, ok, err := r.ClaimPush(ctx, pushConfig())
	if err != nil || ok {
		t.Fatal("cancelled acceptance claimed", d, ok, err)
	}
}
func TestEventIdentityCannotBeReusedAcrossTopics(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			pool := integrationPool(t)
			repo := NewInbox(pool)
			ctx := context.Background()
			order := message(t, events.OrderCreated, 42)
			var e events.Event
			if err := json.Unmarshal(order.Body, &e); err != nil {
				t.Fatal(err)
			}
			offer := offerMessage(t, offers.CourierOfferCreated, "PENDING")
			var fact offers.Event
			if err := json.Unmarshal(offer.Body, &fact); err != nil {
				t.Fatal(err)
			}
			fact.EventID = e.EventID
			body, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			offer.Body = body
			if reverse {
				if err = application.NewBusinessNotifications(repo).Handle(ctx, offer); err != nil {
					t.Fatal(err)
				}
				_, _, err = application.NewNotifications(repo).Handle(ctx, order)
			} else {
				if _, _, err = application.NewNotifications(repo).Handle(ctx, order); err != nil {
					t.Fatal(err)
				}
				err = application.NewBusinessNotifications(repo).Handle(ctx, offer)
			}
			if !errors.Is(err, application.ErrEventConflict) {
				t.Fatal("cross-stream event identity reuse accepted", err)
			}
		})
	}
}
