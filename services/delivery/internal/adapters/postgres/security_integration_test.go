package postgres

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Desu-php/food/platform/security"
	httpadapter "github.com/Desu-php/food/services/delivery/internal/adapters/http"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

func TestMappedCourierCannotCompleteForeignDelivery(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	service := application.NewDelivery(repo, []int64{20})
	owner := makeCourier(t, service)
	other := makeCourier(t, service)
	delivery := accepted(t, repo, 42)
	if _, err := service.Assign(ctx, 20, delivery.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	ready(t, repo, 42)
	if _, err := service.Pickup(ctx, owner.ID, delivery.ID); err != nil {
		t.Fatal(err)
	}
	h := httpadapter.NewHandler(service, pool.Ping, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, false)
	call := func(courier int64) int {
		req := httptest.NewRequest("POST", fmt.Sprintf("/deliveries/%d/complete", delivery.ID), strings.NewReader(`{"payment_confirmed":true,"receipt_reference":"POS-42"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{Subject: fmt.Sprintf("external|%d", courier), UserID: 100 + courier, CourierID: courier, Roles: []string{"courier"}}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if status := call(other.ID); status != 403 {
		t.Fatal("foreign courier completed delivery", status)
	}
	current, err := repo.Get(ctx, owner.ID, delivery.ID)
	if err != nil || current.Status != domain.PickedUp || current.Confirmation != nil {
		t.Fatal("unauthorized attempt changed payment/delivery", current, err)
	}
	if status := call(owner.ID); status != 200 {
		t.Fatal("mapped owner rejected", status)
	}
}
