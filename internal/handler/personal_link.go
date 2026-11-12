package handler

import (
	"context"
	"errors"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
)

// Personal booking links: the two places a link meets the shared cores.
//
//   - personalLinkSlots is computeSlots narrowed to the link's windows.
//   - bookPersonalLink is createBookingForSlug behind the link's single-use guard.
//
// Neither re-implements availability or booking. The windows are a filter applied to what
// the engine already decided is bookable, never a second opinion about what is free - so a
// guest cannot be offered, or talk an assistant into, a time outside them.

// errOutsidePersonalLinkWindows is returned when a booking is asked for at a time the link
// does not offer.
var errOutsidePersonalLinkWindows = errors.New("that time is not one this link offers")

// personalLinks returns the link store. It is built per call because the encryption key is
// set after the handler is constructed; the service itself holds no state worth keeping.
func (h *Handler) personalLinks() *personallink.Service {
	return personallink.New(h.db, h.encKey)
}

// personalLinkEventType returns the slug and length of the link's event type.
func (h *Handler) personalLinkEventType(ctx context.Context, l *personallink.Link) (slug string, minutes int, err error) {
	err = h.db.QueryRowContext(ctx,
		`SELECT slug, duration_minutes FROM event_types WHERE id = ?`, l.EventTypeID).Scan(&slug, &minutes)
	if err != nil {
		return "", 0, errEventTypeNotFound
	}
	return slug, minutes, nil
}

// personalLinkSlots returns the bookable times inside the link's windows, in tzName.
//
// It asks for none of the optional outputs: like every agent-facing caller, the assistant
// that presents these is handed bookable times only (see slotsWanted).
func (h *Handler) personalLinkSlots(ctx context.Context, l *personallink.Link, tzName string) (slotsResult, error) {
	if !l.Active() || len(l.Windows) == 0 {
		return slotsResult{Slots: []slotJSON{}, Hosts: map[string]map[string]string{}}, nil
	}
	slug, _, err := h.personalLinkEventType(ctx, l)
	if err != nil {
		return slotsResult{}, err
	}
	// Ask the engine for a day either side of the windows and filter exactly afterwards.
	// The range is in dates and the windows are instants; a day of slack keeps a window
	// that crosses midnight in the booker's zone from losing its edge.
	first, last := l.Windows[0].Start, l.Windows[0].End
	for _, w := range l.Windows {
		if w.End.After(last) {
			last = w.End
		}
	}
	from := first.UTC().AddDate(0, 0, -1)
	if today := time.Now().UTC().Truncate(24 * time.Hour); from.Before(today) {
		from = today
	}
	want := slotsWanted{}
	if l.PlaceAddress != "" {
		// The same switch the booking page flips when a guest picks in person: in-person
		// buffers, and drive time measured to the confirmed address.
		want.InPerson, want.Location = true, l.PlaceAddress
	}
	res, err := h.computeSlots(ctx, slug, tzName,
		from.Format("2006-01-02"), last.UTC().AddDate(0, 0, 1).Format("2006-01-02"), want)
	if err != nil {
		return slotsResult{}, err
	}
	kept := make([]slotJSON, 0, len(res.Slots))
	for _, s := range res.Slots {
		start, err1 := time.Parse(time.RFC3339, s.Start)
		end, err2 := time.Parse(time.RFC3339, s.End)
		if err1 == nil && err2 == nil && l.Covers(start, end) {
			kept = append(kept, s)
		}
	}
	res.Slots = kept
	return res, nil
}

// bookPersonalLink books startAt through the link, once.
//
// The link is claimed before the booking is attempted and released if the attempt fails, so
// two requests for the same link cannot both reach the booking core. outsideWindows is for
// a time the guest suggested and the owner approved; it skips the window check and nothing
// else - the time still has to be bookable.
func (h *Handler) bookPersonalLink(ctx context.Context, l *personallink.Link, startAt time.Time, organizer booking.Attendee, outsideWindows bool) (*booking.Booking, error) {
	slug, minutes, err := h.personalLinkEventType(ctx, l)
	if err != nil {
		return nil, err
	}
	startAt = startAt.UTC()
	if !outsideWindows && !l.Covers(startAt, startAt.Add(time.Duration(minutes)*time.Minute)) {
		return nil, errOutsidePersonalLinkWindows
	}
	if organizer.Name == "" {
		organizer.Name = l.GuestName
	}
	if organizer.Email == "" {
		organizer.Email = l.GuestEmail
	}

	links := h.personalLinks()
	if err := links.Claim(ctx, l.ID); err != nil {
		return nil, err
	}
	b, err := h.createBookingForSlugWith(ctx, slug, startAt, organizer, nil, bookingExtras{PhysicalLocation: l.PlaceAddress})
	if err != nil {
		if relErr := links.Release(ctx, l.ID); relErr != nil {
			// The link stays claimed until the stale-claim timeout frees it. Not fatal, but
			// it means a guest who retries in the next two minutes is told the link is busy.
			h.logger.ErrorContext(ctx, "personal link: release after failed booking", "link", l.ID, "error", relErr)
		}
		return nil, err
	}
	if err := links.MarkBooked(ctx, l.ID, b.ID); err != nil {
		// The booking exists and stands. The link is left claimed, which still refuses a
		// second booking; what is lost is the pointer from the link to its booking.
		h.logger.ErrorContext(ctx, "personal link: mark booked", "link", l.ID, "booking", b.ID, "error", err)
	}
	return b, nil
}
