package calendar

import (
	"context"
	"time"
)

// EventState is what a provider reports about an event Bookmeet wrote earlier, when asked to
// read it back. The host may have moved or deleted it in their own calendar since.
type EventState int

const (
	// EventUnknown: the provider could not say (no connection, an all-day or unreadable event,
	// an event this account cannot be shown to hold). Callers change nothing.
	EventUnknown EventState = iota
	// EventPresent: the event exists, at Start/End.
	EventPresent
	// EventCancelled: the provider itself says the event was cancelled or deleted.
	EventCancelled
	// EventMissing: the provider answered "no such event". On its own that is ambiguous: a
	// deleted event and an event that lives in a different account look the same. Service.ReadEvent
	// only reports it as gone when the account asked is the one that wrote the event.
	EventMissing
)

// EventSnapshot is the current state of one calendar event. Start and End are set only when
// State is EventPresent.
type EventSnapshot struct {
	State      EventState
	Start, End time.Time
}

// Gone reports whether the event was cancelled or deleted in the calendar.
func (e EventSnapshot) Gone() bool { return e.State == EventCancelled }

// EventReader is implemented by providers that can read an event back. It is separate from
// Provider so a backend without it simply never reports a change.
type EventReader interface {
	// ReadEvent returns the event's current state. calendarID is the one CreateEvent reported
	// ("" for bookings made before that was recorded). A transport or server failure is an
	// error; "no connection" is EventUnknown with a nil error.
	ReadEvent(ctx context.Context, userID, calendarID, eventID string) (EventSnapshot, error)
}

// ReadEvent reads an event back from the provider that holds it, so a move or deletion made in
// the host's own calendar can be applied to the booking. provider and calendarID are the ones
// recorded at creation; account is the connected account that wrote the event
// (booking_hosts.external_account, "" when unknown).
//
// The result is only ever EventUnknown, EventPresent or EventCancelled. A bare "not found" is
// turned into EventCancelled only when it can be trusted: the provider resolves the owning
// account from the event id itself (CalDAV), or the account answering is the recorded one.
// Otherwise a host who swapped one Google or Microsoft account for another would have every
// upcoming booking read as deleted.
func (s *Service) ReadEvent(ctx context.Context, userID, calendarID, eventID, provider, account string) (EventSnapshot, error) {
	if eventID == "" {
		return EventSnapshot{}, nil
	}
	pr := s.providerForEvent(ctx, userID, eventID, provider)
	if pr == nil {
		return EventSnapshot{}, nil
	}
	reader, ok := pr.(EventReader)
	if !ok {
		return EventSnapshot{}, nil
	}
	snap, err := reader.ReadEvent(ctx, userID, calendarID, eventID)
	if err != nil {
		return EventSnapshot{}, err
	}
	if snap.State != EventMissing {
		return snap, nil
	}
	if _, selfResolving := pr.(EventRecognizer); selfResolving {
		return EventSnapshot{State: EventCancelled}, nil
	}
	if account != "" && s.DestinationAccount(ctx, userID, pr.Name()) == account {
		return EventSnapshot{State: EventCancelled}, nil
	}
	return EventSnapshot{}, nil
}

// DestinationAccount returns the account (email) of the user's destination connection when it
// belongs to the named provider, else "". It is the account CreateEvent writes as, so it is
// recorded beside a new event and compared again when that event is read back.
func (s *Service) DestinationAccount(ctx context.Context, userID, provider string) string {
	var account string
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(account_email,'') FROM calendar_connections
		 WHERE user_id = ? AND provider = ? AND is_destination = 1 LIMIT 1`, userID, provider).Scan(&account); err != nil {
		return ""
	}
	return account
}
