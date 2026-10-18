package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/contracts/geo"
	ids "github.com/Desu-php/food/contracts/orders/v1"
	catalogadapter "github.com/Desu-php/food/services/orders/internal/adapters/catalog"
	"github.com/Desu-php/food/services/orders/internal/application"
	"github.com/Desu-php/food/services/orders/internal/domain"
)

func TestCatalogGeolocationSnapshotCommandAndSaga(t *testing.T) {
	pool := integrationPool(t)
	r := NewOrders(pool)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"restaurant_id":1,"currency":"TJS","restaurant_location":{"latitude":38.56,"longitude":68.77},"items":[{"menu_item_id":10,"restaurant_id":1,"name":"Dish","quantity":1,"unit_price_minor":6500,"currency":"TJS"}]}`)
	}))
	defer server.Close()
	client, err := catalogadapter.NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	s := application.NewOrders(r, client)
	request := application.CreateCommand{CustomerID: 7, RestaurantID: 1, FulfillmentType: domain.Delivery, DeliveryAddress: "Customer address", Currency: "TJS", ExpectedSubtotalMinor: 6500, Items: []application.RequestedItem{{MenuItemID: 10, Quantity: 1}}}
	o, err := s.CreateOrder(ctx, request, "geo-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	location := o.Snapshot().RestaurantLocation
	if location == nil || *location != (geo.Point{Latitude: 38.56, Longitude: 68.77}) {
		t.Fatal(location)
	}
	detached := o.Snapshot()
	detached.RestaurantLocation.Latitude = 0
	if o.Snapshot().RestaurantLocation.Latitude != 38.56 {
		t.Fatal("snapshot aliases aggregate")
	}
	loaded, err := r.Get(ctx, 7, o.Snapshot().ID)
	if err != nil || !geo.Equal(location, loaded.Snapshot().RestaurantLocation) {
		t.Fatal(loaded, err)
	}
	staff := application.NewRestaurants(r, map[int64][]int64{10: {1}})
	if _, err = staff.Change(ctx, 10, 1, o.Snapshot().ID, "accept"); err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err = pool.QueryRow(ctx, `SELECT body FROM outbox_events WHERE stream='delivery_commands' AND aggregate_id=$1`, o.Snapshot().ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	command, err := commands.Decode(body)
	if err != nil || !geo.Equal(location, command.Payload.RestaurantLocation) || command.Payload.DispatchDeadline == nil {
		t.Fatal(command, err)
	}
	// Real Inbox transitions: automatic and manual assignment use the same existing events.
	for i, kind := range []string{delivery.DeliveryCreated, delivery.CourierAssigned} {
		fact := delivery.Event{EventID: ids.NewID(), EventType: kind, SchemaVersion: 1, AggregateID: 99, AggregateVersion: int64(i + 1), CorrelationID: "auto-dispatch", OccurredAt: time.Now(), Payload: delivery.Payload{OrderID: o.Snapshot().ID, AmountToCollectMinor: 6500, Currency: "TJS", Status: domainDeliveryStatus(i)}}
		if i == 1 {
			// A pending CourierOffer is local to Delivery and emits no assignment.
			// The restaurant must still wait for the confirmed CourierAssigned fact.
			if _, err = staff.Change(ctx, 10, 1, o.Snapshot().ID, "prepare"); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Fatal("cooking before offer acceptance", err)
			}
			fact.Payload.CourierID = 5
		}
		if err = r.ProcessDelivery(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = staff.Change(ctx, 10, 1, o.Snapshot().ID, "prepare"); err != nil {
		t.Fatal(err)
	}
	// Creation retries keep the original ready-to-review snapshot even when Catalog is offline.
	server.Close()
	retried, err := s.CreateOrder(ctx, request, "geo-snapshot")
	if err != nil || !geo.Equal(location, retried.Snapshot().RestaurantLocation) || retried.Snapshot().Status != domain.WaitingRestaurant {
		t.Fatal(retried, err)
	}
	var original map[string]any
	if err = pool.QueryRow(ctx, `SELECT response FROM idempotency_keys WHERE order_id=$1`, o.Snapshot().ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &original); err != nil || original["RestaurantLocation"] == nil {
		t.Fatal(original, err)
	}
}
func domainDeliveryStatus(i int) string {
	if i == 0 {
		return "PENDING_ASSIGNMENT"
	}
	return "ASSIGNED"
}
