package domain

import (
	"errors"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
)

const Pending = "PENDING"

var ErrInvalidNotification = errors.New("invalid notification intent")

// Notification is an intent; no provider has delivered it to the customer yet.
type Notification struct {
	ID                   int64
	Recipient            Recipient
	Title, Body          string
	Data                 map[string]string
	ExpiresAt            *time.Time
	OfferID              int64
	CreatedAt, UpdatedAt time.Time
	EventID              string
	OrderID              int64
	Version              int64
	CustomerID           int64
	EventType            string
	Status               string
	CorrelationID        string
}

func NewPending(eventID string, orderID, version, customerID int64, kind, correlationID string) (Notification, error) {
	if eventID == "" || orderID <= 0 || customerID <= 0 || correlationID == "" ||
		!events.KnownType(kind) ||
		(kind == "OrderCreated" && version != 1 || kind != "OrderCreated" && version < 2) {
		return Notification{}, ErrInvalidNotification
	}
	return Notification{EventID: eventID, OrderID: orderID, Version: version, CustomerID: customerID, EventType: kind, Status: Pending, CorrelationID: correlationID}, nil
}
