package application

import (
	"errors"
	"time"

	commands "github.com/Desu-php/food/contracts/delivery/commands/v1"
	events "github.com/Desu-php/food/contracts/delivery/v1"
	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/domain"
)

// CommandChange describes one atomic local business operation. The PostgreSQL
// adapter supplies the locked snapshot and persists these effects with Inbox/Outbox.
type CommandChange struct {
	Delivery         domain.Delivery
	Created          *domain.Delivery
	EventType        string
	Reason           string
	Cancelled        bool
	ReleaseCourierID int64
}

func ApplyDeliveryCommand(c commands.Command, current *domain.Delivery, preparation, ready, cancelled bool, now time.Time) (CommandChange, error) {
	if err := c.Validate(); err != nil {
		return CommandChange{}, err
	}
	change := CommandChange{EventType: events.DeliveryCreated, Cancelled: cancelled}
	var d domain.Delivery
	if current == nil {
		var err error
		d, err = domain.NewDelivery(c.AggregateID, c.Payload.AmountToCollectMinor, c.Payload.Currency, c.Payload.OrderVersion, now)
		if err != nil {
			return change, err
		}
		d.DeliveryLocation = geo.Clone(c.Payload.DeliveryLocation)
		d.RestaurantLocation = geo.Clone(c.Payload.RestaurantLocation)
		d.DispatchDeadline = c.Payload.DispatchDeadline
		d.DeliveryAddress = c.Payload.DeliveryAddress
		if preparation {
			d.ObservePreparation()
		}
		if ready {
			if err = d.MarkReady(); err != nil {
				return change, err
			}
		}
	} else {
		d = *current
		if !geo.Equal(d.DeliveryLocation, c.Payload.DeliveryLocation) || d.AmountToCollectMinor != c.Payload.AmountToCollectMinor || d.Currency != c.Payload.Currency || d.DeliveryAddress != c.Payload.DeliveryAddress || !geo.Equal(d.RestaurantLocation, c.Payload.RestaurantLocation) {
			return change, ErrEventConflict
		}
		if c.CommandType == commands.CreateDelivery && c.Payload.DispatchDeadline != nil {
			d.DispatchDeadline = c.Payload.DispatchDeadline
		}
		change.EventType = events.DeliveryStateReported
	}
	if c.CommandType == commands.CancelDelivery || cancelled {
		if current == nil {
			created := d
			change.Created = &created
		}
		old := d
		err := d.Cancel(now)
		switch {
		case errors.Is(err, domain.ErrCancellationUnsafe):
			if d.Status == domain.Delivered {
				d.ReportState(now)
				change.EventType = events.DeliveryStateReported
			} else {
				d.RejectCancellation(now)
				change.EventType = events.DeliveryCancellationRejected
				change.Reason = "physical_fulfillment_started"
			}
		case err != nil:
			return change, err
		default:
			change.Cancelled = true
			if old.Status == domain.Cancelled {
				d.ReportState(now)
				change.EventType = events.DeliveryStateReported
			} else {
				change.EventType = events.DeliveryCancelled
			}
			if old.CourierID > 0 && old.Status == domain.Assigned {
				change.ReleaseCourierID = old.CourierID
			}
		}
	} else if current != nil {
		d.ReportState(now)
	}
	change.Delivery = d
	return change, nil
}
