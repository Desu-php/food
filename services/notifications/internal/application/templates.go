package application

import (
	"fmt"
	"strconv"
	"time"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

// Templates owns presentation, separately from transport and domain invariants.
// Locale selection can be extended without changing event consumption.
func Template(kind, _ string, order int64) (string, string, bool) {
	titles := map[string]string{
		"OrderCreated": "Новый заказ", "OrderAccepted": "Заказ принят", "OrderRejected": "Заказ отклонён", "OrderReadyForPickup": "Заказ готов", "OrderOnTheWay": "Заказ в пути", "OrderDelivered": "Заказ доставлен", "OrderCancelled": "Заказ отменён", "OrderPickedUpByCustomer": "Заказ выдан", "OrderNeedsAttention": "Требуется вмешательство", "CourierOfferCreated": "Новый заказ", "CourierOfferClosed": "Предложение закрыто", "CourierAssigned": "Назначение подтверждено", "DeliveryCancelled": "Доставка отменена",
	}
	bodies := map[string]string{
		"OrderCreated": "Поступил новый заказ №%d", "OrderAccepted": "Ресторан принял ваш заказ №%d", "OrderRejected": "Ресторан отклонил заказ №%d", "OrderReadyForPickup": "Ваш заказ №%d готов к самовывозу", "OrderOnTheWay": "Курьер забрал заказ №%d", "OrderDelivered": "Ваш заказ №%d доставлен", "OrderCancelled": "Заказ №%d отменён", "OrderPickedUpByCustomer": "Заказ №%d получен", "OrderNeedsAttention": "Проверьте состояние заказа №%d", "CourierAssigned": "Вы назначены на доставку заказа №%d", "DeliveryCancelled": "Доставка заказа №%d отменена",
	}
	title, ok := titles[kind]
	if !ok {
		return "", "", false
	}
	if kind == "CourierOfferCreated" {
		return title, "Поступило предложение на доставку", true
	}
	if kind == "CourierOfferClosed" {
		return title, "Предложение доставки больше не действует", true
	}
	return title, fmt.Sprintf(bodies[kind], order), true
}
func Intent(eventID, kind, correlation string, order, version int64, recipient domain.Recipient, occurred time.Time) domain.Notification {
	title, body, ok := Template(kind, "ru", order)
	status := domain.Pending
	if !ok {
		status = domain.Cancelled
	}
	expiry := occurred.Add(24 * time.Hour)
	return domain.Notification{EventID: eventID, OrderID: order, Version: version, EventType: kind, CorrelationID: correlation, Recipient: recipient, Title: title, Body: body, Data: map[string]string{"event_id": eventID, "event_type": kind, "order_id": strconv.FormatInt(order, 10)}, Status: status, ExpiresAt: &expiry}
}
func OrderIntents(e events.Event) []domain.Notification {
	r := domain.Recipient{Type: domain.Customer, ID: e.Payload.CustomerID}
	if e.EventType == events.OrderCreated {
		r = domain.Recipient{Type: domain.Restaurant, ID: e.Payload.RestaurantID}
	}
	n := Intent(e.EventID, e.EventType, e.CorrelationID, e.AggregateID, e.AggregateVersion, r, e.OccurredAt)
	n.CustomerID = e.Payload.CustomerID
	// Readiness for DELIVERY is not a pickup-ready customer notification.
	if e.EventType == events.OrderReadyForPickup && e.Payload.FulfillmentType != "PICKUP" {
		n.Status = domain.Cancelled
	}
	result := []domain.Notification{n}
	if e.EventType == events.OrderCancelled {
		other := n
		other.Recipient = domain.Recipient{Type: domain.Restaurant, ID: e.Payload.RestaurantID}
		result = append(result, other)
	}
	return result
}
