package application

import (
	"context"
	"errors"
	"strconv"

	events "github.com/Desu-php/food/contracts/orders/v1"
	"github.com/Desu-php/food/services/notifications/internal/domain"
)

var ErrEventConflict = errors.New("event ID or aggregate version reused with conflicting content")

type Message struct {
	Topic       string
	Partition   int32
	Offset      int64
	LeaderEpoch int32
	Key         []byte
	Body        []byte
}

type InboxRepository interface {
	Process(context.Context, events.Event, domain.Notification) (bool, error)
	Quarantine(context.Context, Message, string, int) error
}

type Notifications struct{ repo InboxRepository }

func NewNotifications(repo InboxRepository) *Notifications { return &Notifications{repo: repo} }

// Handle creates independent notification intents, so versions can arrive out of
// order without overwriting customer state. Repository uniqueness fences conflicts.
func (s *Notifications) Handle(ctx context.Context, m Message) (events.Event, bool, error) {
	e, err := events.Decode(m.Body)
	if err != nil {
		return e, false, err
	}
	if string(m.Key) != strconv.FormatInt(e.AggregateID, 10) {
		return e, false, events.ErrInvalid
	}
	n, err := domain.NewPending(e.EventID, e.AggregateID, e.AggregateVersion, e.Payload.CustomerID, e.EventType, e.CorrelationID)
	if err != nil {
		return e, false, err
	}
	duplicate, err := s.repo.Process(ctx, e, n)
	return e, duplicate, err
}
