package httpadapter

import (
	"time"

	"github.com/Desu-php/food/services/delivery/internal/domain"
)

type courierDTO struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type confirmationDTO struct {
	ID               int64     `json:"id"`
	OrderID          int64     `json:"order_id"`
	DeliveryID       int64     `json:"delivery_id"`
	AmountMinor      int64     `json:"amount_minor"`
	Currency         string    `json:"currency"`
	PaymentMethod    string    `json:"payment_method"`
	ReceiptReference string    `json:"receipt_reference,omitempty"`
	POSReference     string    `json:"pos_reference,omitempty"`
	ConfirmedBy      int64     `json:"confirmed_by"`
	ConfirmedAt      time.Time `json:"confirmed_at"`
}
type deliveryDTO struct {
	DispatchPaused       bool             `json:"dispatch_paused"`
	DispatchAttempts     int              `json:"dispatch_attempts"`
	DispatchReason       string           `json:"dispatch_reason,omitempty"`
	DispatchNextAt       time.Time        `json:"dispatch_next_at"`
	DispatchDeadline     *time.Time       `json:"dispatch_deadline,omitempty"`
	DeliveryAddress      string           `json:"delivery_address,omitempty"`
	ID                   int64            `json:"id"`
	OrderID              int64            `json:"order_id"`
	CourierID            int64            `json:"courier_id,omitempty"`
	Status               string           `json:"status"`
	AmountToCollectMinor int64            `json:"amount_to_collect_minor"`
	Currency             string           `json:"currency"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
	Version              int64            `json:"version"`
	Ready                bool             `json:"ready_for_pickup"`
	Confirmation         *confirmationDTO `json:"confirmation,omitempty"`
}

func toCourier(c domain.Courier) courierDTO {
	return courierDTO{c.ID, c.Name, c.Status, c.CreatedAt, c.UpdatedAt}
}
func toDelivery(d domain.Delivery) deliveryDTO {
	result := deliveryDTO{DispatchPaused: d.DispatchPaused, DispatchAttempts: d.DispatchAttempts, DispatchReason: d.DispatchReason, DispatchNextAt: d.DispatchNextAt, DispatchDeadline: d.DispatchDeadline, DeliveryAddress: d.DeliveryAddress, ID: d.ID, OrderID: d.OrderID, CourierID: d.CourierID, Status: d.Status, AmountToCollectMinor: d.AmountToCollectMinor, Currency: d.Currency, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Version: d.Version, Ready: d.Ready}
	if p := d.Confirmation; p != nil {
		result.Confirmation = &confirmationDTO{p.ID, p.OrderID, p.DeliveryID, p.AmountMinor, p.Currency, p.PaymentMethod, p.ReceiptReference, p.ReceiptReference, p.ConfirmedBy, p.ConfirmedAt}
	}
	return result
}
