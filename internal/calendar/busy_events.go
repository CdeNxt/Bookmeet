package calendar

import (
	"context"
	"time"
)

// BusyEvent is something on a host's calendar that takes up time: what free/busy reports as
// an interval, with its title.
//
// A title is the one piece of calendar content this program ever reads, and it has exactly
// one reader: the host themselves, when they are asked whether to book over it. It must not
// be shown to a guest, sent to a model, or stored.
type BusyEvent struct {
	Title string
	Start time.Time
	End   time.Time
}

// BusyEventLister is implemented by providers that can name what makes a time busy. It is
// separate from Provider so a backend without it simply reports nothing, and the caller
// falls back to "something on your calendar".
type BusyEventLister interface {
	// BusyEvents returns the events that block time between from and to, on the calendars
	// the host checks for conflicts.
	BusyEvents(ctx context.Context, userID string, from, to time.Time) ([]BusyEvent, error)
}

// BusyEvents names what is on the host's calendars between from and to, from every provider
// that can say. A provider that cannot, or fails, contributes nothing: this is a courtesy to
// the host, never something a decision depends on.
func (s *Service) BusyEvents(ctx context.Context, userID string, from, to time.Time) []BusyEvent {
	var out []BusyEvent
	for _, p := range s.providers {
		lister, ok := p.(BusyEventLister)
		if !ok {
			continue
		}
		events, err := lister.BusyEvents(ctx, userID, from, to)
		if err != nil {
			continue
		}
		out = append(out, events...)
	}
	return out
}
