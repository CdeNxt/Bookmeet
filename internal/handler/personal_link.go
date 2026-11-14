package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
)

// Personal booking links: the two places a link meets the shared cores.
//
//   - personalLinkSlots is computeSlots narrowed to the link's windows.
//   - bookPersonalLink is createBookingForSlug behind the link's single-use guard.
//
//   - suggestedTimeVerdict and bookPersonalLinkApproved are for a time the guest suggested:
//     whether it would book, and booking it on the owner's say-so when it would not.
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
	// An exception the owner allowed on this link: their hours, notice and booking horizon
	// are set aside inside its windows. Their calendar and bookings are not.
	want.OwnerException = l.OutsideAvailability
	want.OwnerArranged = true // the owner chose these dates; their booking horizon is not for them
	res, err := h.computeSlots(ctx, slug, tzName,
		from.Format("2006-01-02"), last.UTC().AddDate(0, 0, 1).Format("2006-01-02"), want)
	if errors.Is(err, errBadDateRange) {
		// Every window has passed, or lies beyond the engine's one-year reach: nothing to offer.
		return slotsResult{Slots: []slotJSON{}, Hosts: map[string]map[string]string{}}, nil
	}
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

// windowSlotCounts says, for each window an owner wants to offer, how many times the engine
// would offer inside it as things stand, and how many with the owner's own rules set aside.
// The difference is what an exception would add; zero in both means they are busy throughout.
func (h *Handler) windowSlotCounts(ctx context.Context, slug, tzName string, windows []personallink.Window, place string) (normal, exception []int, err error) {
	normal, exception = make([]int, len(windows)), make([]int, len(windows))
	if len(windows) == 0 {
		return normal, exception, nil
	}
	first, last := windows[0].Start, windows[0].End
	for _, w := range windows {
		if w.Start.Before(first) {
			first = w.Start
		}
		if w.End.After(last) {
			last = w.End
		}
	}
	from := first.UTC().AddDate(0, 0, -1)
	if today := time.Now().UTC().Truncate(24 * time.Hour); from.Before(today) {
		from = today
	}
	want := slotsWanted{OwnerArranged: true} // as personalLinkSlots asks
	if place != "" {
		want.InPerson, want.Location = true, place
	}
	for pass, counts := range [][]int{normal, exception} {
		want.OwnerException = pass == 1
		res, err := h.computeSlots(ctx, slug, tzName, from.Format("2006-01-02"), last.UTC().AddDate(0, 0, 1).Format("2006-01-02"), want)
		if errors.Is(err, errBadDateRange) {
			continue // the whole range is beyond how far ahead the owner takes bookings
		}
		if err != nil {
			return nil, nil, err
		}
		for _, slot := range res.Slots {
			start, err1 := time.Parse(time.RFC3339, slot.Start)
			end, err2 := time.Parse(time.RFC3339, slot.End)
			if err1 != nil || err2 != nil {
				continue
			}
			for i, w := range windows {
				if !start.Before(w.Start) && !end.After(w.End) {
					counts[i]++
				}
			}
		}
	}
	return normal, exception, nil
}

// bookPersonalLink books startAt through the link, once.
//
// The link is claimed before the booking is attempted and released if the attempt fails, so
// two requests for the same link cannot both reach the booking core. outsideWindows is for
// a time the guest suggested and the owner approved; it skips the window check and nothing
// else - the time still has to be bookable.
func (h *Handler) bookPersonalLink(ctx context.Context, l *personallink.Link, startAt time.Time, organizer booking.Attendee, outsideWindows bool) (*booking.Booking, error) {
	return h.bookPersonalLinkWith(ctx, l, startAt, organizer, outsideWindows, false)
}

// bookPersonalLinkApproved books a time the guest suggested and the owner chose to book even
// though their own rules or calendar say no. It is the one path that books over a calendar
// clash, and it exists only behind the owner's "Book anyway" tap. The link's single-use
// guard and the booking service's own overlap guard both still apply.
func (h *Handler) bookPersonalLinkApproved(ctx context.Context, l *personallink.Link, startAt time.Time, organizer booking.Attendee) (*booking.Booking, error) {
	return h.bookPersonalLinkWith(ctx, l, startAt, organizer, true, true)
}

// timeVerdict says why a time would not book as things stand, for the owner's eyes only.
// More than one can be true: a time can be outside the owner's hours and clash as well.
type timeVerdict struct {
	Outside bool // outside the owner's hours, notice or booking horizon
	Clash   bool // something on the owner's calendar, or another Bookmeet booking, is there
	Unknown bool // the calendar could not be checked
	// Conflicts names what is in the way, when it can be named. The titles come from the
	// owner's own calendar and are for the owner alone: never the guest, never a model,
	// never stored.
	Conflicts []timeConflict
}

type timeConflict struct {
	Title string
	Start time.Time
	End   time.Time
}

func (v timeVerdict) free() bool { return !v.Outside && !v.Clash && !v.Unknown }

// note is the short marker that goes on a button.
func (v timeVerdict) note() string {
	var parts []string
	if v.Outside {
		parts = append(parts, "outside your hours")
	}
	switch {
	case v.Clash && len(v.Conflicts) > 0:
		parts = append(parts, "clashes with "+v.Conflicts[0].Title)
	case v.Clash:
		parts = append(parts, "clashes with your calendar")
	case v.Unknown:
		parts = append(parts, "calendar not checked")
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, ", ")
}

// suggestedTimeVerdict says whether a time the guest suggested would book right now, and if
// not, why and over what. It asks the booking core itself (planBooking), so the answer is
// the one a booking attempt would get. It is for the owner's Telegram message and nothing
// else: the guest and the model are never told, or a suggestion would be a way to probe the
// calendar.
func (h *Handler) suggestedTimeVerdict(ctx context.Context, l *personallink.Link, startAt time.Time) timeVerdict {
	slug, minutes, err := h.personalLinkEventType(ctx, l)
	if err != nil {
		return timeVerdict{Unknown: true}
	}
	return h.timeVerdictFor(ctx, slug, minutes, startAt, l.PlaceAddress, l.UserID)
}

// timeVerdictFor is the verdict on one start time for an event type: would it book, and if
// not, why and over what. The same rule about who may see the answer applies.
func (h *Handler) timeVerdictFor(ctx context.Context, slug string, minutes int, startAt time.Time, place, ownerID string) timeVerdict {
	startAt = startAt.UTC()
	endAt := startAt.Add(time.Duration(minutes) * time.Minute)
	// The checks do not depend on who is asking, only on the time.
	organizer := booking.Attendee{Name: "Guest", Email: "guest@example.invalid"}
	var err error
	var v timeVerdict
	// First against the owner's own rules; then with those waived, so a time that is
	// outside their hours is still checked against their calendar.
	_, params, err := h.planBooking(ctx, slug, startAt, organizer, nil, bookingExtras{PhysicalLocation: place, OwnerInvited: true})
	switch {
	case err == nil:
	case errors.Is(err, errCalendarClash):
		v.Clash = true
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		v.Outside = true
		_, params, err = h.planBooking(ctx, slug, startAt, organizer, nil, bookingExtras{PhysicalLocation: place, OwnerInvited: true, OutsideRules: true})
		switch {
		case err == nil:
		case errors.Is(err, errCalendarClash):
			v.Clash = true
		default:
			v.Unknown = true
		}
	default:
		v.Unknown = true
	}
	hosts := params.HostIDs
	if len(hosts) == 0 {
		hosts = []string{ownerID}
	}
	// Another Bookmeet booking in the way is a clash too, and one that can be named.
	if overlaps, err := h.bookingSvc.Overlapping(ctx, hosts, startAt, endAt); err == nil {
		for _, o := range overlaps {
			v.Clash = true
			title := o.EventTypeName
			if o.GuestName != "" {
				title += " with " + o.GuestName
			}
			v.Conflicts = append(v.Conflicts, timeConflict{Title: title, Start: o.StartAt, End: o.EndAt})
		}
	}
	// What is on the calendar then, by name, where the provider can say.
	if cal := h.getCal(); v.Clash && cal != nil {
		for _, hostID := range hosts {
			for _, e := range cal.BusyEvents(ctx, hostID, startAt, endAt) {
				if e.Start.Before(endAt) && e.End.After(startAt) {
					v.Conflicts = append(v.Conflicts, timeConflict{Title: e.Title, Start: e.Start, End: e.End})
				}
			}
		}
	}
	return v
}

func (h *Handler) bookPersonalLinkWith(ctx context.Context, l *personallink.Link, startAt time.Time, organizer booking.Attendee, outsideWindows, ownerApproved bool) (*booking.Booking, error) {
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
	b, err := h.createBookingForSlugWith(ctx, slug, startAt, organizer, nil, bookingExtras{
		PhysicalLocation: l.PlaceAddress,
		OwnerInvited:     true,                  // the owner made this link for this guest
		OutsideRules:     l.OutsideAvailability, // an exception the owner allowed on the link
		OwnerApproved:    ownerApproved,
		OverBookings:     ownerApproved,
	})
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
