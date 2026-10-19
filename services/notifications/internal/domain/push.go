package domain

import (
	"errors"
	"strings"
	"time"
)

const (
	Processing = "PROCESSING"
	Sent       = "SENT"
	Failed     = "FAILED"
	Cancelled  = "CANCELLED"
	Customer   = "CUSTOMER"
	Restaurant = "RESTAURANT"
	Courier    = "COURIER"
)

var ErrInvalidDevice = errors.New("invalid device")
var ErrNotFound = errors.New("resource not found")
var ErrLeaseLost = errors.New("push lease lost")

type Recipient struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

func (r Recipient) Valid() bool {
	return r.ID > 0 && (r.Type == Customer || r.Type == Restaurant || r.Type == Courier)
}

type DeviceRegistration struct {
	ID             int64     `json:"id"`
	OwnerType      string    `json:"owner_type"`
	OwnerID        int64     `json:"owner_id"`
	RegisteredBy   int64     `json:"-"`
	Platform       string    `json:"platform"`
	InstallationID string    `json:"-"`
	IsActive       bool      `json:"is_active"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (d DeviceRegistration) Validate() error {
	if !(Recipient{d.OwnerType, d.OwnerID}).Valid() || d.RegisteredBy <= 0 || (d.Platform != "IOS" && d.Platform != "ANDROID" && d.Platform != "WEB") || len(d.InstallationID) < 10 || len(d.InstallationID) > 2048 || strings.ContainsAny(d.InstallationID, " \t\r\n\x00") {
		return ErrInvalidDevice
	}
	return nil
}

type PushMessage struct {
	Title, Body string
	Data        map[string]string
	ExpiresAt   time.Time
	CollapseKey string
}
type Delivery struct {
	ID, NotificationID, DeviceID      int64
	Recipient                         Recipient
	EventID, EventType, CorrelationID string
	OfferID                           int64
	Message                           PushMessage
	Token                             string
	Attempts                          int
	CreatedAt                         time.Time
}

// ProviderResult contains only sanitized classifications. Never persist raw provider errors.
type ProviderResult struct {
	MessageID                string
	Code                     string
	Temporary, InvalidDevice bool
	RetryAfter               time.Duration
}

// DeliveryStatus applies the retry budget to a sanitized provider result.
func DeliveryStatus(result ProviderResult, attempt, maximum int) string {
	switch {
	case result.Code == "accepted":
		return Sent
	case result.Code == "cancelled":
		return Cancelled
	case result.Temporary && attempt < maximum:
		return Pending
	default:
		return Failed
	}
}
