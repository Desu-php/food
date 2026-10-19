package application

import (
	"strings"
	"testing"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

func TestBusinessTemplates(t *testing.T) {
	for _, kind := range []string{events.OrderAccepted, events.OrderRejected, events.OrderOnTheWay, events.OrderDelivered, events.OrderCancelled, events.OrderReadyForPickup, "CourierOfferCreated", "CourierOfferClosed", "CourierAssigned", "DeliveryCancelled", "OrderNeedsAttention"} {
		title, body, ok := Template(kind, "ru", 123)
		if !ok || title == "" || body == "" {
			t.Fatal(kind)
		}
		if strings.Contains(body, "адрес") || strings.Contains(body, "POS") {
			t.Fatal("sensitive template")
		}
	}
	e := events.Event{EventID: events.NewID(), EventType: events.OrderReadyForPickup, AggregateID: 123, AggregateVersion: 4, OccurredAt: time.Now(), Payload: events.Payload{CustomerID: 8, RestaurantID: 2, FulfillmentType: "PICKUP"}}
	n := OrderIntents(e)
	if len(n) != 1 || n[0].Recipient.Type != domain.Customer || n[0].Status != domain.Pending || !strings.Contains(n[0].Body, "самовывоз") {
		t.Fatal(n)
	}
	e.Payload.FulfillmentType = "DELIVERY"
	if OrderIntents(e)[0].Status != domain.Cancelled {
		t.Fatal("delivery readiness sent as pickup")
	}
	e.EventType = events.OrderCreated
	if OrderIntents(e)[0].Recipient.Type != domain.Restaurant {
		t.Fatal("wrong recipient")
	}
	e.EventType = events.OrderCancelled
	if len(OrderIntents(e)) != 2 {
		t.Fatal("missing restaurant cancellation")
	}
}
func TestRetryDelayBounded(t *testing.T) {
	for attempt := 1; attempt < 100; attempt++ {
		d := retryDelay(attempt, time.Second, time.Minute)
		if d < 500*time.Millisecond || d > time.Minute {
			t.Fatal(d)
		}
	}
}
