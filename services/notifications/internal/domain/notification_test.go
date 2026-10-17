package domain

import "testing"

func TestNotificationIntent(t *testing.T) {
	n, err := NewPending("event", 1, 1, 2, "OrderCreated", "request")
	if err != nil || n.Status != Pending {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := NewPending("event", 1, 1, 2, "OrderCancelled", "request"); err != ErrInvalidNotification {
		t.Fatal("accepted mismatched version")
	}
	if _, err := NewPending("event", 1, 1, 0, "OrderCreated", "request"); err != ErrInvalidNotification {
		t.Fatal("accepted invalid customer")
	}
}
