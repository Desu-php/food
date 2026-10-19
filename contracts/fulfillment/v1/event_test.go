package fulfillment

import (
	ids "github.com/Desu-php/food/contracts/orders/v1"
	"testing"
	"time"
)

func TestAttentionContract(t *testing.T) {
	e := Event{EventID: ids.NewID(), EventType: OrderNeedsAttention, SchemaVersion: 1, AggregateID: 42, AggregateVersion: 5, CorrelationID: "attention", OccurredAt: time.Now(), Payload: Payload{OrderID: 42, RestaurantID: 1}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Payload.OrderID = 43
	if e.Validate() == nil {
		t.Fatal("wrong aggregate accepted")
	}
	if _, err := Decode([]byte(`{"schema_version":99}`)); err == nil {
		t.Fatal("unsupported version accepted")
	}
}
