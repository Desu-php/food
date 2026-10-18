package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	orderevents "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func pendingOffer(t *testing.T, r *Repository, courier int64) domain.CourierOffer {
	t.Helper()
	ctx := context.Background()
	cfg := application.DefaultDispatchConfig()
	claim, found, err := r.ClaimDispatch(ctx, cfg)
	if err != nil || !found {
		t.Fatal(claim, found, err)
	}
	if offered, err := r.OfferDispatch(ctx, claim, candidates(courier), cfg.Cooldown); err != nil || !offered {
		t.Fatal(offered, err)
	}
	offers, err := r.Offers(ctx, courier)
	if err != nil || len(offers) != 1 {
		t.Fatal(offers, err)
	}
	return offers[0]
}
func storedOffer(t *testing.T, r *Repository, id int64) domain.CourierOffer {
	t.Helper()
	o, err := scanOffer(r.pool.QueryRow(context.Background(), `SELECT `+offerColumns+` FROM courier_offers WHERE id=$1`, id))
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func expireFixture(t *testing.T, r *Repository, id int64) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(), `UPDATE courier_offers SET created_at=now()-interval '31 seconds',expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}
func assignmentCount(t *testing.T, r *Repository, want int) {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE event_type='CourierAssigned'`).Scan(&n); err != nil || n != want {
		t.Fatal(n, want, err)
	}
}

func TestOfferAcceptanceAtomicRetryAndConcurrentResponses(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	before, err := r.Get(ctx, 0, d.ID)
	if err != nil || before.Status != domain.PendingAssignment || before.CourierID != 0 {
		t.Fatal(before, err)
	}
	assignmentCount(t, r, 0)
	if err := r.RetryDispatch(ctx, d.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	// Force the Outbox write to fail: offer, courier and delivery must roll back.
	if _, err = r.pool.Exec(ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_assignment CHECK(event_type<>'CourierAssigned') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RespondOffer(ctx, c.ID, o.ID, true); err == nil {
		t.Fatal("accepted without Outbox")
	}
	if storedOffer(t, r, o.ID).Status != domain.OfferPending {
		t.Fatal("decision escaped rollback")
	}
	before, err = r.Get(ctx, 0, d.ID)
	if err != nil || before.Status != domain.PendingAssignment {
		t.Fatal(before, err)
	}
	if err = r.CourierAvailable(ctx, c.ID); err != nil {
		t.Fatal("courier escaped rollback", err)
	}
	if _, err = r.pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT reject_assignment`); err != nil {
		t.Fatal(err)
	}
	// Replicas race on the same offer. Both exact retries succeed, with one event.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := NewRepository(r.pool).RespondOffer(ctx, c.ID, o.ID, true)
			if err != nil || got.Status != domain.OfferAccepted {
				t.Error(got, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	assignmentCount(t, r, 1)
	audit := storedOffer(t, r, o.ID)
	if audit.RespondedAt == nil {
		t.Fatal(audit)
	}
	again, err := r.RespondOffer(ctx, c.ID, o.ID, true)
	if err != nil || !again.RespondedAt.Equal(*audit.RespondedAt) {
		t.Fatal(again, err)
	}
	if _, err = r.RespondOffer(ctx, c.ID, o.ID, false); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, c.ID, d.ID)
	if err != nil || got.Status != domain.Assigned || got.Version != 2 {
		t.Fatal(got, err)
	}
	if err = r.CourierAvailable(ctx, c.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
}

func TestOfferDeclineExpiryCooldownAndRestart(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint("expire=", expire), func(t *testing.T) {
			r := NewRepository(integrationPool(t))
			ctx := context.Background()
			s := application.NewDelivery(r, []int64{20})
			d := geoDelivery(t, r, 42)
			c1, c2 := makeCourier(t, s), makeCourier(t, s)
			o := pendingOffer(t, r, c1.ID)
			// A newly constructed worker/repository must see the committed reservation.
			restarted := NewRepository(r.pool)
			cfg := application.DefaultDispatchConfig()
			if _, found, err := restarted.ClaimDispatch(ctx, cfg); err != nil || found {
				t.Fatal(found, err)
			}
			if expire {
				expireFixture(t, r, o.ID)
				list, err := restarted.Offers(ctx, c1.ID)
				if err != nil || len(list) != 0 {
					t.Fatal(list, err)
				}
				if found, err := restarted.ExpireOffer(ctx); err != nil || !found {
					t.Fatal(found, err)
				}
				if storedOffer(t, r, o.ID).Status != domain.OfferExpired {
					t.Fatal("expiry not persisted")
				}
				if _, err := r.RespondOffer(ctx, c1.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
					t.Fatal(err)
				}
			} else {
				first, err := restarted.RespondOffer(ctx, c1.ID, o.ID, false)
				if err != nil || first.Status != domain.OfferDeclined {
					t.Fatal(first, err)
				}
				again, err := restarted.RespondOffer(ctx, c1.ID, o.ID, false)
				if err != nil || !again.RespondedAt.Equal(*first.RespondedAt) {
					t.Fatal(again, err)
				}
			}
			if found, err := restarted.ExpireOffer(ctx); err != nil || found {
				t.Fatal(found, err)
			}
			// The next closest courier is selected, with no reset of retry budget.
			worker := application.NewDispatch(restarted, &fixedLocations{candidates: candidates(c1.ID, c2.ID)}, cfg)
			if worked, err := worker.Step(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			list, err := restarted.Offers(ctx, c2.ID)
			if err != nil || len(list) != 1 || list[0].DeliveryID != d.ID {
				t.Fatal(list, err)
			}
			assignmentCount(t, r, 0)
		})
	}
}

func TestOfferExpiryOnResponseAndDeadline(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	if _, err := r.pool.Exec(ctx, `UPDATE deliveries SET dispatch_deadline=clock_timestamp()+interval '5 seconds' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	o := pendingOffer(t, r, c.ID)
	if o.ExpiresAt.Sub(o.CreatedAt) > 5*time.Second {
		t.Fatal("TTL extended Saga deadline", o)
	}
	expireFixture(t, r, o.ID)
	if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	if storedOffer(t, r, o.ID).Status != domain.OfferExpired {
		t.Fatal("late accept did not persist expiry")
	}
	assignmentCount(t, r, 0)
}

func TestOfferCancellationByCommandAndOrderEvent(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(fmt.Sprint("command=", command), func(t *testing.T) {
			r := NewRepository(integrationPool(t))
			ctx := context.Background()
			s := application.NewDelivery(r, []int64{20})
			d := geoDelivery(t, r, 42)
			c := makeCourier(t, s)
			o := pendingOffer(t, r, c.ID)
			if command {
				cancel := createCommand(42, 2, commands.CancelDelivery)
				cancel.Payload.RestaurantLocation = d.RestaurantLocation
				cancel.Payload.DispatchDeadline = d.DispatchDeadline
				if err := r.ProcessCommand(ctx, cancel); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, e := range []orderevents.Event{orderEvent(42, 2, orderevents.OrderAccepted), orderEvent(42, 3, orderevents.OrderCancellationRequested)} {
					if err := r.ProcessOrder(ctx, e); err != nil {
						t.Fatal(err)
					}
				}
			}
			if storedOffer(t, r, o.ID).Status != domain.OfferCancelled {
				t.Fatal("pending offer survived cancellation")
			}
			if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
				t.Fatal(err)
			}
			assignmentCount(t, r, 0)
			if err := r.CourierAvailable(ctx, c.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOfferManualAssignmentRacesAcceptance(t *testing.T) {
	for _, sameCourier := range []bool{false, true} {
		t.Run(fmt.Sprint("same_courier=", sameCourier), func(t *testing.T) {
			r := NewRepository(integrationPool(t))
			ctx := context.Background()
			s := application.NewDelivery(r, []int64{20})
			d := geoDelivery(t, r, 42)
			c, manual := makeCourier(t, s), makeCourier(t, s)
			if sameCourier {
				manual = c
			}
			o := pendingOffer(t, r, c.ID)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, err := r.RespondOffer(ctx, c.ID, o.ID, true)
				if err != nil && !errors.Is(err, domain.ErrConflict) {
					t.Error(err)
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				_, err := s.Assign(ctx, 20, d.ID, manual.ID)
				if err != nil && !errors.Is(err, domain.ErrConflict) {
					t.Error(err)
				}
			}()
			close(start)
			wg.Wait()
			assignmentCount(t, r, 1)
			audit := storedOffer(t, r, o.ID)
			if audit.Status != domain.OfferAccepted && audit.Status != domain.OfferCancelled {
				t.Fatal(audit)
			}
			if audit.Status == domain.OfferCancelled {
				if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestManualAssignmentCancelsOwnOfferAndProtectsOtherReservation(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	d := geoDelivery(t, r, 42)
	other := geoDelivery(t, r, 43)
	c, manual := makeCourier(t, s), makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	if _, err := s.Assign(ctx, 20, other.ID, c.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.Assign(ctx, 20, d.ID, manual.ID); err != nil {
		t.Fatal(err)
	}
	if storedOffer(t, r, o.ID).Status != domain.OfferCancelled {
		t.Fatal("manual assignment retained offer")
	}
	if _, err := s.Assign(ctx, 20, other.ID, c.ID); err != nil {
		t.Fatal("reservation not released", err)
	}
}

func TestOfferSQLConstraintsAndCourierIsolation(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20}).WithOffers(application.NewOffers(r))
	d := geoDelivery(t, r, 42)
	otherDelivery := geoDelivery(t, r, 43)
	c, other := makeCourier(t, s), makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	for _, pair := range [][2]int64{{d.ID, other.ID}, {otherDelivery.ID, c.ID}} {
		_, err := r.pool.Exec(ctx, `INSERT INTO courier_offers(delivery_id,courier_id,status,created_at,expires_at) VALUES($1,$2,'PENDING',now(),now()+interval '30 seconds')`, pair[0], pair[1])
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23505" {
			t.Fatal("missing unique constraint", err)
		}
	}
	h := httpadapter.NewHandler(s, r.pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	for _, tc := range []struct {
		method, path string
		courier      int64
		code         int
	}{
		{"GET", "/couriers/me/offers", other.ID, 200},
		{"POST", fmt.Sprintf("/courier-offers/%d/accept", o.ID), other.ID, 404},
		{"POST", fmt.Sprintf("/courier-offers/%d/decline", o.ID), other.ID, 404},
		{"POST", fmt.Sprintf("/courier-offers/%d/accept", o.ID), c.ID, 200},
		{"POST", fmt.Sprintf("/courier-offers/%d/accept", o.ID), c.ID, 200},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{Subject: "mapped", CourierID: tc.courier, Roles: []string{"courier"}}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatal(tc, w.Code, w.Body)
		}
		if tc.method == "GET" && w.Body.String() != "[]\n" {
			t.Fatal("foreign offer leaked", w.Body)
		}
	}
	assignmentCount(t, r, 1)
}

func TestOfferAcceptanceRespectsQuarantineAndOfflineCourier(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	if _, err := r.pool.Exec(ctx, `INSERT INTO consumer_quarantine(topic,partition,offset_value,message_key,body,reason,attempts) VALUES('delivery.commands.v1',0,1,''::bytea,''::bytea,'unsupported_contract',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	if storedOffer(t, r, o.ID).Status != domain.OfferPending {
		t.Fatal("fenced request changed offer")
	}
	if _, err := r.pool.Exec(ctx, `UPDATE consumer_quarantine SET resolved_at=now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetAvailability(ctx, c.ID, c.ID, domain.Offline); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	if storedOffer(t, r, o.ID).Status != domain.OfferCancelled {
		t.Fatal("offline offer remained active")
	}
	assignmentCount(t, r, 0)
}

func TestConcurrentExpiryWorkersAndAcceptance(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	expireFixture(t, r, o.ID)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := NewRepository(r.pool).ExpireOffer(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if _, err := r.RespondOffer(ctx, c.ID, o.ID, true); !errors.Is(err, domain.ErrConflict) {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	if storedOffer(t, r, o.ID).Status != domain.OfferExpired {
		t.Fatal("expiry lost")
	}
	assignmentCount(t, r, 0)
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_declines WHERE delivery_id=$1 AND courier_id=$2`, o.DeliveryID, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestOfferDecisionDoesNotResetExhaustedBudget(t *testing.T) {
	r := NewRepository(integrationPool(t))
	ctx := context.Background()
	s := application.NewDelivery(r, []int64{20})
	d := geoDelivery(t, r, 42)
	c := makeCourier(t, s)
	o := pendingOffer(t, r, c.ID)
	if _, err := r.RespondOffer(ctx, c.ID, o.ID, false); err != nil {
		t.Fatal(err)
	}
	cfg := application.DefaultDispatchConfig()
	cfg.MaxAttempts = 1
	if _, found, err := r.ClaimDispatch(ctx, cfg); err != nil || found {
		t.Fatal(found, err)
	}
	got, err := r.Get(ctx, 0, d.ID)
	if err != nil || !got.DispatchPaused || got.DispatchAttempts != 1 || got.DispatchReason != "attempts_exhausted:offer_declined" {
		t.Fatal(got, err)
	}
}
