package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	offers "github.com/Desu-php/food/contracts/courieroffers/v1"
	delivery "github.com/Desu-php/food/contracts/delivery/v1"
	fulfillment "github.com/Desu-php/food/contracts/fulfillment/v1"
	orders "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

type OfferFact struct {
	ID        int64
	Terminal  bool
	ExpiresAt time.Time
}
type BusinessEvent struct {
	ID, Kind, CorrelationID, Stream string
	AggregateID, Version            int64
	Canonical                       []byte
	Intents                         []domain.Notification
	Offer                           *OfferFact
}
type BusinessInboxRepository interface {
	ProcessBusiness(context.Context, BusinessEvent) error
}
type BusinessNotifications struct{ repo BusinessInboxRepository }

func NewBusinessNotifications(repo BusinessInboxRepository) *BusinessNotifications {
	return &BusinessNotifications{repo}
}
func (s *BusinessNotifications) Handle(ctx context.Context, m Message) error {
	e, err := businessEvent(m)
	if err != nil {
		return err
	}
	return s.repo.ProcessBusiness(ctx, e)
}
func businessEvent(m Message) (BusinessEvent, error) {
	var canonical any
	var eventID, kind, correlation, stream string
	var aggregate, version int64
	var intents []domain.Notification
	var offer *OfferFact
	switch m.Topic {
	case offers.Topic:
		e, err := offers.Decode(m.Body)
		if err != nil {
			return BusinessEvent{}, orders.ErrInvalid
		}
		offer = &OfferFact{ID: e.Payload.OfferID, Terminal: e.EventType == offers.CourierOfferClosed, ExpiresAt: e.Payload.ExpiresAt}
		canonical = e
		eventID, kind, correlation, stream, aggregate, version = e.EventID, e.EventType, e.CorrelationID, offers.Topic, e.AggregateID, e.AggregateVersion
		n := Intent(eventID, kind, correlation, e.Payload.OrderID, version, domain.Recipient{Type: domain.Courier, ID: e.Payload.CourierID}, e.OccurredAt)
		n.OfferID = e.Payload.OfferID
		if kind == offers.CourierOfferCreated {
			n.ExpiresAt = &e.Payload.ExpiresAt
		}
		intents = []domain.Notification{n}
		// Acceptance is represented by the existing CourierAssigned fact; a closed
		// accepted/declined offer needs no extra notification to its responding courier.
		if kind == offers.CourierOfferClosed && (e.Payload.Status == "ACCEPTED" || e.Payload.Status == "DECLINED" || e.Payload.Status == "EXPIRED") {
			intents = nil
		}
	case fulfillment.Topic:
		e, err := fulfillment.Decode(m.Body)
		if err != nil {
			return BusinessEvent{}, orders.ErrInvalid
		}
		canonical = e
		eventID, kind, correlation, stream, aggregate, version = e.EventID, e.EventType, e.CorrelationID, fulfillment.Topic, e.AggregateID, e.AggregateVersion
		intents = []domain.Notification{Intent(eventID, kind, correlation, e.Payload.OrderID, version, domain.Recipient{Type: domain.Restaurant, ID: e.Payload.RestaurantID}, e.OccurredAt)}
	case delivery.Topic:
		e, err := delivery.Decode(m.Body)
		if err != nil {
			return BusinessEvent{}, orders.ErrInvalid
		}
		canonical = e
		eventID, kind, correlation, stream, aggregate, version = e.EventID, e.EventType, e.CorrelationID, delivery.Topic, e.AggregateID, e.AggregateVersion
		if (kind == delivery.CourierAssigned || kind == delivery.DeliveryCancelled) && e.Payload.CourierID > 0 {
			intents = []domain.Notification{Intent(eventID, kind, correlation, e.Payload.OrderID, version, domain.Recipient{Type: domain.Courier, ID: e.Payload.CourierID}, e.OccurredAt)}
		}
	default:
		return BusinessEvent{}, orders.ErrUnsupported
	}
	if string(m.Key) != strconv.FormatInt(aggregate, 10) {
		return BusinessEvent{}, orders.ErrInvalid
	}

	body, err := json.Marshal(canonical)
	if err != nil {
		return BusinessEvent{}, fmt.Errorf("encode business event fingerprint: %w", err)
	}
	return BusinessEvent{ID: eventID, Kind: kind, CorrelationID: correlation, Stream: stream, AggregateID: aggregate, Version: version, Canonical: body, Intents: intents, Offer: offer}, nil
}
