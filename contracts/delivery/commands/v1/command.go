// Package commands defines directed fulfillment commands, separate from domain events.
package commands

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Desu-php/food/contracts/correlation"
	"github.com/Desu-php/food/contracts/geo"
	orders "github.com/Desu-php/food/contracts/orders/v1"
)

const (
	Topic          = "delivery.commands.v1"
	CreateDelivery = "CreateDelivery"
	CancelDelivery = "CancelDelivery"
)

var ErrUnsupported = errors.New("unsupported delivery contract")

var ErrInvalid = errors.New("invalid delivery command")

type Payload struct {
	RestaurantLocation   *geo.Point `json:"restaurant_location,omitempty"`
	DispatchDeadline     *time.Time `json:"dispatch_deadline,omitempty"`
	FulfillmentType      string     `json:"fulfillment_type,omitempty"`
	DeliveryAddress      string     `json:"delivery_address,omitempty"`
	OrderID              int64      `json:"order_id"`
	OrderVersion         int64      `json:"order_version"`
	AmountToCollectMinor int64      `json:"amount_to_collect_minor"`
	Currency             string     `json:"currency"`
}
type Command struct {
	MessageID        string    `json:"message_id"`
	CommandType      string    `json:"command_type"`
	CorrelationID    string    `json:"correlation_id"`
	AggregateID      int64     `json:"aggregate_id"`
	AggregateVersion int64     `json:"aggregate_version"`
	SchemaVersion    int       `json:"schema_version"`
	OccurredAt       time.Time `json:"occurred_at"`
	Payload          Payload   `json:"payload"`
}

func (c Command) Validate() error {
	if c.Payload.RestaurantLocation != nil && !c.Payload.RestaurantLocation.Valid() || c.Payload.DispatchDeadline != nil && c.Payload.DispatchDeadline.IsZero() {
		return ErrInvalid
	}
	if c.Payload.FulfillmentType != "" && c.Payload.FulfillmentType != "DELIVERY" || c.Payload.FulfillmentType == "DELIVERY" && c.Payload.DeliveryAddress == "" {
		return ErrInvalid
	}
	if a := c.Payload.DeliveryAddress; a != "" && (strings.TrimSpace(a) != a || !utf8.ValidString(a) || strings.ContainsRune(a, 0) || utf8.RuneCountInString(a) > 500) {
		return ErrInvalid
	}
	if !orders.ValidID(c.MessageID) || !correlation.Valid(c.CorrelationID) || c.AggregateID <= 0 || c.AggregateVersion < 1 || c.SchemaVersion != 1 || c.OccurredAt.IsZero() || c.Payload.OrderID != c.AggregateID || c.Payload.OrderVersion < 2 || c.Payload.AmountToCollectMinor <= 0 || c.Payload.Currency != "TJS" || c.CommandType != CreateDelivery && c.CommandType != CancelDelivery {
		return ErrInvalid
	}
	return nil
}
func Decode(body []byte) (Command, error) {
	var c Command
	if len(body) > orders.MaxMessageBytes || json.Unmarshal(body, &c) != nil {
		return c, ErrInvalid
	}
	if c.SchemaVersion != 1 || !slices.Contains([]string{CreateDelivery, CancelDelivery}, c.CommandType) {
		return c, errors.Join(ErrInvalid, ErrUnsupported)
	}
	return c, c.Validate()
}
