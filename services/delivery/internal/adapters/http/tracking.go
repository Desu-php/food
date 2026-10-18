package httpadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Desu-php/food/platform/security"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

type TrackingConfig struct {
	PollInterval, Heartbeat, MaxDuration, WriteTimeout, OperationTimeout time.Duration
	MaxConnections, MaxPerCustomer                                       int
}

func DefaultTrackingConfig() TrackingConfig {
	return TrackingConfig{PollInterval: 2 * time.Second, Heartbeat: 15 * time.Second, MaxDuration: 5 * time.Minute, WriteTimeout: 5 * time.Second, OperationTimeout: 2 * time.Second, MaxConnections: 200, MaxPerCustomer: 3}
}

// Admission is per API process, with no goroutines or unbounded waiter queue.
type trackingConnections struct {
	mu        sync.Mutex
	total     int
	customers map[int64]int
}

func (c *trackingConnections) acquire(customer int64, cfg TrackingConfig) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total >= cfg.MaxConnections || c.customers[customer] >= cfg.MaxPerCustomer {
		return false
	}
	if c.customers == nil {
		c.customers = make(map[int64]int)
	}
	c.total++
	c.customers[customer]++
	return true
}
func (c *trackingConnections) release(customer int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total--
	c.customers[customer]--
	if c.customers[customer] == 0 {
		delete(c.customers, customer)
	}
}

func (h *handler) customer(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if p, ok := security.FromContext(r.Context()); ok && p.Subject != "" {
		if p.Has("customer") && p.CustomerID > 0 {
			return p.CustomerID, true
		}
		writeJSON(w, 403, map[string]string{"error": "access denied"})
		return 0, false
	}
	if h.devAuth && len(r.Header.Values("X-Dev-Customer-ID")) == 1 && r.Header.Get("X-Dev-Courier-ID") == "" && r.Header.Get("X-Dev-User-ID") == "" {
		customer, err := strconv.ParseInt(r.Header.Get("X-Dev-Customer-ID"), 10, 64)
		if err == nil && customer > 0 {
			return customer, true
		}
	}
	writeJSON(w, 401, map[string]string{"error": "authentication required"})
	return 0, false
}

func (h *handler) trackingSnapshot(ctx context.Context, customer, order int64) (application.TrackingUpdate, error) {
	op, cancel := context.WithTimeout(ctx, h.trackingConfig.OperationTimeout)
	defer cancel()
	return h.service.Track(op, customer, order)
}

func trackingFrame(update application.TrackingUpdate) string {
	var data any = struct {
		Status string `json:"status"`
	}{update.Status}
	if l := update.Location; l != nil {
		// Explicit allowlist: no courier identity, internal timestamps or order data.
		data = struct {
			Latitude       float64   `json:"latitude"`
			Longitude      float64   `json:"longitude"`
			AccuracyMeters float64   `json:"accuracy_meters"`
			RecordedAt     time.Time `json:"recorded_at"`
		}{l.Latitude, l.Longitude, l.AccuracyMeters, l.RecordedAt}
	}
	body, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", update.Event, body)
}

func (h *handler) tracking(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	// Authentication uses the Authorization header, never an URL query token.
	if r.URL.RawQuery != "" {
		writeJSON(w, 400, map[string]string{"error": "tracking query parameters are not supported"})
		return
	}
	customer, ok := h.customer(w, r)
	if !ok {
		return
	}
	order, ok := id(w, r)
	if !ok {
		return
	}
	if !h.connections.acquire(customer, h.trackingConfig) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, 429, map[string]string{"error": "tracking connection limit exceeded"})
		return
	}
	defer h.connections.release(customer)
	update, err := h.trackingSnapshot(r.Context(), customer, order)
	if err != nil {
		h.fail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "streaming unavailable"})
		return
	}
	controller := http.NewResponseController(w)
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(frame string) bool {
		if r.Context().Err() != nil {
			return false
		}
		if err := controller.SetWriteDeadline(time.Now().Add(h.trackingConfig.WriteTimeout)); err != nil && err != http.ErrNotSupported {
			return false
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		flusher.Flush()
		return r.Context().Err() == nil
	}
	previous := trackingFrame(update)
	if !write("retry: 3000\n\n"+previous) || update.Event == "tracking_ended" {
		return
	}
	poll := time.NewTicker(h.trackingConfig.PollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(h.trackingConfig.Heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if !write(": heartbeat\n\n") {
				return
			}
		case <-poll.C:
			update, err := h.trackingSnapshot(r.Context(), customer, order)
			if err != nil {
				write("event: tracking_unavailable\ndata: {}\n\n")
				return
			}
			frame := trackingFrame(update)
			if frame != previous {
				if !write(frame) {
					return
				}
				previous = frame
			}
			if update.Event == "tracking_ended" {
				return
			}
		}
	}
}
