package domain

import (
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Desu-php/food/contracts/geo"
)

const (
	CurrencyTJS                        = "TJS"
	PendingPayment     Status          = "PENDING_PAYMENT"
	Cancelled          Status          = "CANCELLED"
	WaitingRestaurant  Status          = "WAITING_RESTAURANT"
	Accepted           Status          = "ACCEPTED"
	WaitingCourier     Status          = "WAITING_COURIER"
	Preparing          Status          = "PREPARING"
	ReadyForPickup     Status          = "READY_FOR_PICKUP"
	OnTheWay           Status          = "ON_THE_WAY"
	Delivered          Status          = "DELIVERED"
	PickedUpByCustomer Status          = "PICKED_UP_BY_CUSTOMER"
	Delivery           FulfillmentType = "DELIVERY"
	Pickup             FulfillmentType = "PICKUP"
	POSAtPickup                        = "POS_AT_PICKUP"
	POSOnDelivery                      = "POS_ON_DELIVERY"
	Unpaid                             = "UNPAID"
	ReportedCollected                  = "REPORTED_COLLECTED"
	MaxItems                           = 100
	MaxQuantity                        = 100
)

type Status string
type FulfillmentType string

var (
	ErrInvalidOrder       = errors.New("positive customer and restaurant IDs, TJS currency and 1 to 100 unique items are required")
	ErrInvalidItem        = errors.New("each item requires a positive ID, price and quantity between 1 and 100")
	ErrRestaurantMismatch = errors.New("all items must belong to the order restaurant")
	ErrOverflow           = errors.New("order amount exceeds int64 range")
	ErrCannotCancel       = errors.New("order can only be cancelled before preparation under its fulfillment policy")
	ErrInvalidTransition  = errors.New("invalid order state transition")
	ErrPaymentMismatch    = errors.New("invalid POS collection confirmation")
	ErrNotFound           = errors.New("order not found")
)

// Order is the aggregate root. Only its methods may change business state.
// Snapshots are detached values; changing them cannot mutate an existing order.
type Order struct{ state State }

type State struct {
	TotalMinor            int64            `json:",omitempty"`
	DeliveryLocation      *geo.Point       `json:",omitempty"`
	PricingSnapshot       *PricingSnapshot `json:",omitempty"`
	RestaurantLocation    *geo.Point       `json:",omitempty"`
	ID                    int64
	Version               int64
	CustomerID            int64
	RestaurantID          int64
	Status                Status
	CourierID             int64
	CancellationRequested bool
	FulfillmentType       FulfillmentType
	DeliveryAddress       string
	PickupAddress         string
	LegacyDeliveryAddress bool
	DeliveryFeeMinor      int64
	PaymentMethod         string
	PaymentStatus         string
	Currency              string
	SubtotalMinor         int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Items                 []OrderItem
}

type OrderItem struct {
	ID              int64
	OrderID         int64
	MenuItemID      int64
	Name            string
	Quantity        int64
	UnitPriceMinor  int64
	TotalPriceMinor int64
}

// PricedItem comes from a trusted catalog port, never from the client request.
type PricedItem struct {
	MenuItemID     int64
	RestaurantID   int64
	Name           string
	Quantity       int64
	UnitPriceMinor int64
	Currency       string
}

func NewOrder(customerID, restaurantID int64, currency string, items []PricedItem, now time.Time, fulfillment Fulfillment) (Order, error) {
	return newOrder(customerID, restaurantID, currency, items, now, fulfillment, false)
}

func newOrder(customerID, restaurantID int64, currency string, items []PricedItem, now time.Time, fulfillment Fulfillment, legacyAddress bool) (Order, error) {
	if customerID <= 0 || restaurantID <= 0 || currency != CurrencyTJS || len(items) == 0 || len(items) > MaxItems || now.IsZero() {
		return Order{}, ErrInvalidOrder
	}
	s := State{Version: 1, CustomerID: customerID, RestaurantID: restaurantID, Currency: currency, Status: WaitingRestaurant, PaymentMethod: POSOnDelivery, PaymentStatus: Unpaid, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), Items: make([]OrderItem, 0, len(items))}
	seen := make(map[int64]bool, len(items))
	for _, p := range items {
		if p.RestaurantID != restaurantID {
			return Order{}, ErrRestaurantMismatch
		}
		if p.Currency != currency {
			return Order{}, ErrInvalidOrder
		}
		if p.MenuItemID <= 0 || seen[p.MenuItemID] || p.Quantity < 1 || p.Quantity > MaxQuantity || p.UnitPriceMinor <= 0 || !utf8.ValidString(p.Name) || strings.TrimSpace(p.Name) == "" || strings.ContainsRune(p.Name, 0) || utf8.RuneCountInString(p.Name) > 200 {
			return Order{}, ErrInvalidItem
		}
		seen[p.MenuItemID] = true
		if p.UnitPriceMinor > math.MaxInt64/p.Quantity {
			return Order{}, ErrOverflow
		}
		total := p.UnitPriceMinor * p.Quantity
		if s.SubtotalMinor > math.MaxInt64-total {
			return Order{}, ErrOverflow
		}
		s.SubtotalMinor += total
		s.Items = append(s.Items, OrderItem{MenuItemID: p.MenuItemID, Name: p.Name, Quantity: p.Quantity, UnitPriceMinor: p.UnitPriceMinor, TotalPriceMinor: total})
	}
	s.TotalMinor = s.SubtotalMinor
	if fulfillment.DeliveryFeeMinor < 0 || s.SubtotalMinor > math.MaxInt64-fulfillment.DeliveryFeeMinor {
		return Order{}, ErrOverflow
	}
	s.DeliveryFeeMinor = fulfillment.DeliveryFeeMinor
	s.TotalMinor += s.DeliveryFeeMinor
	s.DeliveryLocation = geo.Clone(fulfillment.DeliveryLocation)
	s.PricingSnapshot = clonePricing(fulfillment.PricingSnapshot)
	s.RestaurantLocation = geo.Clone(fulfillment.RestaurantLocation)
	s.FulfillmentType = fulfillment.Type
	s.DeliveryAddress = fulfillment.DeliveryAddress
	s.PickupAddress = fulfillment.PickupAddress
	s.LegacyDeliveryAddress = legacyAddress
	if s.FulfillmentType == Pickup {
		s.PaymentMethod = POSAtPickup
	}
	if err := validateFulfillment(s); err != nil {
		return Order{}, err
	}
	return Order{state: s}, nil
}

func (o Order) Snapshot() State {
	s := o.state
	s.Items = slices.Clone(s.Items)
	s.RestaurantLocation = geo.Clone(s.RestaurantLocation)
	s.DeliveryLocation = geo.Clone(s.DeliveryLocation)
	s.PricingSnapshot = clonePricing(s.PricingSnapshot)
	return s
}

// Restore validates persisted state and copies its items before accepting it.
func Restore(s State) (Order, error) {
	if s.Version == 0 {
		s.Version = 1
		if s.Status == Cancelled {
			s.Version = 2
		}
	}
	// Historical creation responses retain PENDING_PAYMENT and absent payment fields.
	if s.PaymentMethod == "" {
		s.PaymentMethod = POSOnDelivery
	}
	if s.PaymentStatus == "" {
		s.PaymentStatus = Unpaid
	}
	if s.FulfillmentType == "" {
		s.FulfillmentType = Delivery
		s.LegacyDeliveryAddress = true
	}
	if err := validateFulfillment(s); err != nil {
		return Order{}, err
	}
	if s.Version < 1 || (s.PaymentStatus != Unpaid && s.PaymentStatus != ReportedCollected) || (s.Status == Delivered || s.Status == PickedUpByCustomer) != (s.PaymentStatus == ReportedCollected) {
		return Order{}, ErrInvalidOrder
	}
	// Versions count facts, including reassignment; historical v1 snapshots remain readable.
	if s.CourierID < 0 || (s.Status == WaitingRestaurant || s.Status == PendingPayment) && s.Version != 1 {
		return Order{}, ErrInvalidOrder
	}
	minimum := map[Status]int64{PendingPayment: 1, WaitingRestaurant: 1, Accepted: 2, WaitingCourier: 3, Preparing: 3, ReadyForPickup: 4, OnTheWay: 5, Delivered: 6, PickedUpByCustomer: 5, Cancelled: 2}
	if s.Version < minimum[s.Status] || (s.Status == WaitingRestaurant || s.Status == PendingPayment) && (s.CourierID != 0 || s.CancellationRequested) {
		return Order{}, ErrInvalidOrder
	}
	items := make([]PricedItem, len(s.Items))
	for i, p := range s.Items {
		if p.ID <= 0 || p.OrderID != s.ID {
			return Order{}, ErrInvalidOrder
		}
		items[i] = PricedItem{p.MenuItemID, s.RestaurantID, p.Name, p.Quantity, p.UnitPriceMinor, s.Currency}
	}
	o, err := newOrder(s.CustomerID, s.RestaurantID, s.Currency, items, s.CreatedAt, Fulfillment{Type: s.FulfillmentType, DeliveryAddress: s.DeliveryAddress, PickupAddress: s.PickupAddress, RestaurantLocation: s.RestaurantLocation, DeliveryLocation: s.DeliveryLocation, DeliveryFeeMinor: s.DeliveryFeeMinor, PricingSnapshot: s.PricingSnapshot}, s.LegacyDeliveryAddress)
	if err != nil {
		return Order{}, err
	}
	if s.ID <= 0 || s.UpdatedAt.Before(s.CreatedAt) || !validStatus(s.Status) || s.SubtotalMinor != o.state.SubtotalMinor {
		return Order{}, ErrInvalidOrder
	}
	for i, p := range s.Items {
		if p.TotalPriceMinor != o.state.Items[i].TotalPriceMinor {
			return Order{}, ErrInvalidOrder
		}
	}
	s.Items = slices.Clone(s.Items)
	s.RestaurantLocation = geo.Clone(s.RestaurantLocation)
	s.DeliveryLocation = geo.Clone(s.DeliveryLocation)
	s.PricingSnapshot = clonePricing(s.PricingSnapshot)
	return Order{state: s}, nil
}

func (o *Order) Cancel(now time.Time) error {
	if o.state.Status != PendingPayment && o.state.Status != WaitingRestaurant && !(o.state.FulfillmentType == Pickup && o.state.Status == Accepted) {
		return ErrCannotCancel
	}
	if now.Before(o.state.UpdatedAt) {
		now = o.state.UpdatedAt
	}
	o.state.Version++
	o.state.Status = Cancelled
	o.state.UpdatedAt = now.UTC()
	return nil
}

func validStatus(s Status) bool {
	switch s {
	case PendingPayment, WaitingRestaurant, Accepted, WaitingCourier, Preparing, ReadyForPickup, OnTheWay, Delivered, PickedUpByCustomer, Cancelled:
		return true
	}
	return false
}

func (o *Order) Transition(target Status, now time.Time) error {
	allowed := o.state.Status == WaitingRestaurant && (target == Accepted || target == Cancelled) ||
		o.state.Status == Accepted && target == Preparing && (o.state.FulfillmentType == Pickup || o.state.CourierID > 0) && !o.state.CancellationRequested || o.state.Status == Preparing && target == ReadyForPickup ||
		o.state.Status == ReadyForPickup && target == OnTheWay && o.state.FulfillmentType == Delivery && o.state.CourierID > 0
	if !allowed {
		return ErrInvalidTransition
	}
	o.advance(target, now)
	return nil
}

func (o *Order) Complete(amount int64, currency, method string, now time.Time) error {
	if o.state.FulfillmentType != Delivery || amount != o.state.AmountToCollect() || currency != o.state.Currency || method != POSOnDelivery {
		return ErrPaymentMismatch
	}
	if o.state.Status != OnTheWay || o.state.PaymentStatus != Unpaid {
		return ErrInvalidTransition
	}
	o.state.CancellationRequested = false
	o.state.PaymentStatus = ReportedCollected
	o.advance(Delivered, now)
	return nil
}
func (o *Order) advance(target Status, now time.Time) {
	o.state.Status = target
	o.state.Version++
	if now.Before(o.state.UpdatedAt) {
		now = o.state.UpdatedAt
	}
	o.state.UpdatedAt = now.UTC()
}

// DeliveryCreated is acknowledged separately from restaurant acceptance.
func (o *Order) AwaitCourier(now time.Time) error {
	if o.state.FulfillmentType != Delivery || o.state.Status != Accepted || o.state.CourierID != 0 {
		return ErrInvalidTransition
	}
	o.advance(WaitingCourier, now)
	return nil
}
func (o *Order) AssignCourier(courier int64, now time.Time) error {
	if o.state.FulfillmentType != Delivery || courier <= 0 || o.state.Status != WaitingCourier && o.state.Status != Accepted && o.state.Status != Preparing && o.state.Status != ReadyForPickup {
		return ErrInvalidTransition
	}
	o.state.CourierID = courier
	target := o.state.Status
	if target == WaitingCourier {
		target = Accepted
	}
	o.advance(target, now)
	return nil
}
func (o *Order) ReleaseCourier(now time.Time) error {
	if o.state.FulfillmentType != Delivery || o.state.CourierID == 0 || o.state.Status != Accepted && o.state.Status != Preparing && o.state.Status != ReadyForPickup {
		return ErrInvalidTransition
	}
	o.state.CourierID = 0
	target := o.state.Status
	if target == Accepted {
		target = WaitingCourier
	}
	o.advance(target, now)
	return nil
}
func (o Order) PreparationStarted() bool {
	return o.state.Status == Preparing || o.state.Status == ReadyForPickup || o.state.Status == OnTheWay || o.state.Status == Delivered || o.state.Status == PickedUpByCustomer
}
func (o *Order) RequestCancellation(now time.Time) error {
	if o.state.FulfillmentType != Delivery {
		return ErrCannotCancel
	}
	if o.state.CancellationRequested {
		return nil
	}
	if o.state.Status != Accepted && o.state.Status != WaitingCourier {
		return ErrCannotCancel
	}
	o.state.CancellationRequested = true
	o.advance(o.state.Status, now)
	return nil
}
func (o *Order) ConfirmCancellation(now time.Time) error {
	if o.state.FulfillmentType != Delivery {
		return ErrCannotCancel
	}
	if o.state.Status == Cancelled {
		return nil
	}
	if o.PreparationStarted() || (o.state.Status != Accepted && o.state.Status != WaitingCourier) {
		return ErrCannotCancel
	}
	o.state.CancellationRequested = false
	o.advance(Cancelled, now)
	return nil
}
func (o *Order) Resume(now time.Time) error {
	if o.state.FulfillmentType != Delivery || o.state.Status == Cancelled || o.state.Status == Delivered || o.state.Status == WaitingRestaurant {
		return ErrInvalidTransition
	}
	if o.state.CancellationRequested {
		o.state.CancellationRequested = false
		o.advance(o.state.Status, now)
	}
	return nil
}

// ConfirmPickup reconciles a courier ID missing from the pre-Saga schema only when
// the orchestrator explicitly identifies a migrated active order.
func (o *Order) ConfirmPickup(courier int64, legacy bool, now time.Time) error {
	if o.state.FulfillmentType != Delivery || courier <= 0 || o.state.Status != ReadyForPickup || o.state.CourierID != courier && !(legacy && o.state.CourierID == 0) {
		return ErrInvalidTransition
	}
	o.state.CourierID = courier
	o.advance(OnTheWay, now)
	return nil
}

// Fulfillment is immutable after creation; addresses are snapshots of the agreed locations.
type Fulfillment struct {
	DeliveryFeeMinor   int64
	DeliveryLocation   *geo.Point
	PricingSnapshot    *PricingSnapshot
	RestaurantLocation *geo.Point
	Type               FulfillmentType
	DeliveryAddress    string
	PickupAddress      string
}

func ValidAddress(s string) bool {
	return s == strings.TrimSpace(s) && s != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= 500
}
func validateFulfillment(s State) error {
	if s.RestaurantLocation != nil && (!s.RestaurantLocation.Valid() || s.FulfillmentType != Delivery) {
		return ErrInvalidOrder
	}
	if s.DeliveryFeeMinor < 0 || s.SubtotalMinor > math.MaxInt64-s.DeliveryFeeMinor || s.TotalMinor != 0 && s.TotalMinor != s.SubtotalMinor+s.DeliveryFeeMinor {
		return ErrInvalidOrder
	}
	if s.DeliveryFeeMinor != 0 && s.PricingSnapshot == nil {
		return ErrInvalidOrder
	}
	if s.TotalMinor == 0 && (s.PricingSnapshot != nil || s.DeliveryFeeMinor != 0) {
		return ErrInvalidOrder
	}
	if s.DeliveryLocation != nil && (!s.DeliveryLocation.Valid() || s.FulfillmentType != Delivery) {
		return ErrInvalidOrder
	}
	if p := s.PricingSnapshot; p != nil {
		if p.SettingsVersion <= 0 || p.MinimumDeliverySubtotalMinor < 0 || p.DeliveryFeeMinor != s.DeliveryFeeMinor {
			return ErrInvalidOrder
		}
		if s.FulfillmentType == Delivery && (s.RestaurantLocation == nil || s.DeliveryLocation == nil || p.TariffID <= 0 || p.DistanceMeters == nil || *p.DistanceMeters < 0 || p.MaxDistanceMeters <= 0 || p.MaxDistanceMeters < *p.DistanceMeters || s.SubtotalMinor < p.MinimumDeliverySubtotalMinor) {
			return ErrInvalidOrder
		}
		if s.FulfillmentType == Pickup && (p.TariffID != 0 || p.MaxDistanceMeters != 0 || p.DistanceMeters != nil) {
			return ErrInvalidOrder
		}
	}
	switch s.FulfillmentType {
	case Delivery:
		if s.PaymentMethod != POSOnDelivery || s.PickupAddress != "" || (!ValidAddress(s.DeliveryAddress) && !(s.LegacyDeliveryAddress && s.DeliveryAddress == "")) || s.Status == PickedUpByCustomer {
			return ErrInvalidOrder
		}
	case Pickup:
		if s.DeliveryFeeMinor != 0 || s.DeliveryLocation != nil || s.PaymentMethod != POSAtPickup || s.DeliveryAddress != "" || s.LegacyDeliveryAddress || !ValidAddress(s.PickupAddress) || s.CourierID != 0 || s.CancellationRequested || s.Status == WaitingCourier || s.Status == OnTheWay || s.Status == Delivered {
			return ErrInvalidOrder
		}
	default:
		return ErrInvalidOrder
	}
	return nil
}

// CompletePickup records an operational statement, never a bank-verified payment.
// The application must authorize staff and verify the customer's token before calling it.
func (o *Order) CompletePickup(confirmed bool, now time.Time) error {
	if !confirmed {
		return ErrPaymentMismatch
	}
	if o.state.FulfillmentType != Pickup || o.state.Status != ReadyForPickup || o.state.PaymentStatus != Unpaid {
		return ErrInvalidTransition
	}
	o.state.PaymentStatus = ReportedCollected
	o.advance(PickedUpByCustomer, now)
	return nil
}
