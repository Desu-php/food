package httpadapter

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type trackingFixture struct {
	mu       sync.Mutex
	state    application.TrackingState
	location domain.Location
	redisErr error
}

func (f *trackingFixture) TrackingState(_ context.Context, customer, order int64) (application.TrackingState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if customer <= 0 || order != customer+35 {
		return application.TrackingState{}, domain.ErrNotFound
	}
	return f.state, nil
}
func (f *trackingFixture) CourierLocationAllowed(context.Context, int64) error { return nil }
func (f *trackingFixture) Store(_ context.Context, l domain.Location) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.location = l
	return nil
}
func (f *trackingFixture) Latest(context.Context, int64) (domain.Location, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.location, true, f.redisErr
}
func (f *trackingFixture) change(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

func trackingHandler(t *testing.T) (*trackingFixture, http.Handler) {
	t.Helper()
	now := time.Now().UTC()
	f := &trackingFixture{state: application.TrackingState{CourierID: 9, Status: domain.PickedUp, Version: 3}, location: domain.Location{CourierID: 9, Latitude: 40.28, Longitude: 69.62, AccuracyMeters: 12, RecordedAt: now, ReceivedAt: now}}
	s := application.NewDelivery(nil, nil).WithTracking(application.NewTracking(f, f, time.Minute, 100))
	cfg := DefaultTrackingConfig()
	cfg.PollInterval = 5 * time.Millisecond
	cfg.Heartbeat = 10 * time.Millisecond
	cfg.MaxDuration = 2 * time.Second
	cfg.MaxConnections = 2
	cfg.MaxPerCustomer = 1
	h := NewHandler(s, func(context.Context) error { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)), 20*time.Millisecond, true, cfg)
	return f, h
}

func readSSE(t *testing.T, reader *bufio.Reader, event string) string {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for %s: %v", event, err)
		}
		if strings.TrimSpace(line) == "event: "+event {
			data, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
	}
}

func openTracking(t *testing.T, server *httptest.Server, customer int) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/orders/%d/tracking", server.URL, customer+35), nil)
	req.Header.Set("X-Dev-Customer-ID", fmt.Sprint(customer))
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close(); client.CloseIdleConnections() })
	return res, bufio.NewReader(res.Body)
}

func TestSSEUpdatesReconnectHeartbeatAndCompletion(t *testing.T) {
	f, h := trackingHandler(t)
	server := httptest.NewServer(h)
	defer server.Close()
	res, reader := openTracking(t, server, 7)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(res.Status, res.Header)
	}
	if data := readSSE(t, reader, "location"); !strings.Contains(data, "40.28") || strings.Contains(data, "courier_id") || strings.Contains(data, "received_at") {
		t.Fatal(data)
	}
	f.change(func() { f.location.Latitude = 40.29; f.location.RecordedAt = time.Now().UTC() })
	if data := readSSE(t, reader, "location"); !strings.Contains(data, "40.29") {
		t.Fatal(data)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, ": heartbeat") {
			break
		}
	}
	res.Body.Close()
	// Retry admission while the server observes the prior disconnect.
	var again *http.Response
	for i := 0; i < 100; i++ {
		again, reader = openTracking(t, server, 7)
		if again.StatusCode == 200 {
			break
		}
		again.Body.Close()
		time.Sleep(time.Millisecond)
	}
	if again.StatusCode != 200 {
		t.Fatal(again.Status)
	}
	if data := readSSE(t, reader, "location"); !strings.Contains(data, "40.29") {
		t.Fatal(data)
	}
	f.change(func() { f.redisErr = application.ErrLocationUnavailable })
	readSSE(t, reader, "location_unavailable")
	f.change(func() { f.redisErr = nil; f.location.Latitude = 40.30 })
	readSSE(t, reader, "location")
	f.change(func() { f.state.Status = domain.Delivered; f.state.Version++ })
	readSSE(t, reader, "tracking_ended")
	if rest, err := io.ReadAll(reader); err != nil || strings.Contains(string(rest), "latitude") {
		t.Fatal(string(rest), err)
	}
}

func TestSSECancellationReassignmentAndTerminalPrivacy(t *testing.T) {
	for _, status := range []string{domain.Cancelled, domain.Delivered, domain.Assigned} {
		t.Run(status, func(t *testing.T) {
			f, h := trackingHandler(t)
			f.change(func() { f.state.Status = domain.Assigned })
			server := httptest.NewServer(h)
			defer server.Close()
			res, reader := openTracking(t, server, 7)
			defer res.Body.Close()
			if data := readSSE(t, reader, "tracking_pending"); strings.Contains(data, "latitude") {
				t.Fatal(data)
			}
			f.change(func() { f.state.Status = status; f.state.CourierID = 10; f.state.Version++ })
			if status == domain.Assigned {
				f.change(func() { f.state.Status = domain.PickedUp; f.state.Version++ })
				if data := readSSE(t, reader, "location_unavailable"); strings.Contains(data, "latitude") {
					t.Fatal("previous courier leaked", data)
				}
			} else {
				readSSE(t, reader, "tracking_ended")
				res.Body.Close()
				// A fresh connection must not read Redis or disclose terminal coordinates.
				res2, r2 := openTracking(t, server, 8)
				defer res2.Body.Close()
				if data := readSSE(t, r2, "tracking_ended"); strings.Contains(data, "latitude") {
					t.Fatal(data)
				}
			}
		})
	}
}

func TestTrackingAuthenticationOwnershipAndNoQueryTokens(t *testing.T) {
	_, h := trackingHandler(t)
	for _, tc := range []struct {
		path string
		p    security.Principal
		code int
	}{
		{"/orders/42/tracking", security.Principal{}, 401},
		{"/orders/42/tracking?access_token=secret", security.Principal{}, 400},
		{"/orders/42/tracking", security.Principal{Subject: "courier", CourierID: 9, Roles: []string{"courier"}}, 403},
		{"/orders/42/tracking", security.Principal{Subject: "other", CustomerID: 8, Roles: []string{"customer"}}, 404},
		{"/orders/999/tracking", security.Principal{Subject: "owner", CustomerID: 7, Roles: []string{"customer"}}, 404},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req = req.WithContext(security.WithPrincipal(req.Context(), tc.p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "latitude") {
			t.Fatal(w.Code, w.Body)
		}
	}
}

func TestSSEConnectionLimitsAndDisconnectCleanup(t *testing.T) {
	_, h := trackingHandler(t)
	var active atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { active.Add(1); defer active.Add(-1); h.ServeHTTP(w, r) }))
	defer server.Close()
	first, _ := openTracking(t, server, 7)
	duplicate, _ := openTracking(t, server, 7)
	if duplicate.StatusCode != 429 {
		t.Fatal(duplicate.Status)
	}
	duplicate.Body.Close()
	second, _ := openTracking(t, server, 8)
	third, _ := openTracking(t, server, 9)
	if first.StatusCode != 200 || second.StatusCode != 200 || third.StatusCode != 429 {
		t.Fatal(first.Status, second.Status, third.Status)
	}
	first.Body.Close()
	second.Body.Close()
	third.Body.Close()
	waitIdle := func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for active.Load() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if active.Load() != 0 {
			t.Fatal("stream handler leaked after disconnect")
		}
	}
	waitIdle()
	for i := 0; i < 30; i++ {
		res, _ := openTracking(t, server, 7)
		if res.StatusCode != 200 {
			t.Fatal("admission slot leaked", res.Status)
		}
		res.Body.Close()
		waitIdle()
	}
}

func TestTrackingGPSValidationAndServerMappedIdentity(t *testing.T) {
	f, h := trackingHandler(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	p := security.Principal{Subject: "opaque", UserID: 100, CourierID: 9, Roles: []string{"courier"}}
	for _, tc := range []struct {
		body string
		code int
	}{
		{fmt.Sprintf(`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":%q}`, now), 204},
		{fmt.Sprintf(`{"latitude":91,"longitude":69.62,"accuracy_meters":12,"recorded_at":%q}`, now), 400},
		{fmt.Sprintf(`{"latitude":40.28,"longitude":69.62,"accuracy_meters":101,"recorded_at":%q}`, now), 400},
		{`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":"invalid"}`, 400},
		{`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":"2020-01-01T00:00:00Z"}`, 400},
		{`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12}`, 400},
		{`{"latitude":40.28,"longitude":69.62,"accuracy_meters":null,"recorded_at":null}`, 400},
		{fmt.Sprintf(`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":%q,"courier_id":99}`, now), 400},
		{fmt.Sprintf(`{"latitude":40.28,"longitude":69.62,"accuracy_meters":12,"recorded_at":%q,"received_at":%q}`, now, now), 400},
	} {
		req := httptest.NewRequest("POST", "/couriers/me/location", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Dev-Courier-ID", "99")
		req = req.WithContext(security.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body)
		}
		if tc.code == 204 && f.location.CourierID != 9 {
			t.Fatal("client identity used")
		}
	}
}

func TestTrackingAdmissionConcurrent(t *testing.T) {
	var connections trackingConnections
	cfg := DefaultTrackingConfig()
	cfg.MaxConnections = 4
	cfg.MaxPerCustomer = 1
	var wg sync.WaitGroup
	var admitted atomic.Int64
	release := make(chan struct{})
	ready := make(chan struct{}, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok := connections.acquire(int64(i%10+1), cfg)
			if ok {
				admitted.Add(1)
			}
			ready <- struct{}{}
			<-release
			if ok {
				connections.release(int64(i%10 + 1))
			}
		}(i)
	}
	for i := 0; i < 40; i++ {
		<-ready
	}
	if admitted.Load() != 4 {
		t.Errorf("admission limit violated: %d", admitted.Load())
	}
	close(release)
	wg.Wait()
	if connections.total != 0 || len(connections.customers) != 0 {
		t.Fatal("admission state leaked")
	}
}

func TestSSEServerContextCancellation(t *testing.T) {
	_, h := trackingHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewUnstartedServer(h)
	server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	server.Start()
	defer server.Close()
	res, reader := openTracking(t, server, 7)
	defer res.Body.Close()
	readSSE(t, reader, "location")
	cancel()
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal("stream failed to close on server cancellation", err)
	}
}
