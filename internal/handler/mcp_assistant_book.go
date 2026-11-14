package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
)

// book_for_guest is the Telegram assistant's "Book it" (bookDirect) for an outside agent: one
// exact time, for a guest whose email the owner gave, booked as the owner's own invitation
// (OwnerInvited lifts the per-guest limits that stop guests over-booking themselves). It goes
// through createBookingForSlugWith like every booking. What it never does is the Allow tap:
// a time outside the owner's hours, or over something on their calendar, is refused and the
// agent is told only which of the two it was, never what is on the calendar.

type bookForGuestIn struct {
	GuestName  string `json:"guest_name" jsonschema:"the guest's full name"`
	GuestEmail string `json:"guest_email" jsonschema:"the guest's email, as the owner gave it; the invitation goes there"`
	EventType  string `json:"event_type" jsonschema:"slug of one of the owner's meeting types (list_event_types)"`
	Date       string `json:"date" jsonschema:"the day, YYYY-MM-DD, in the owner's time zone"`
	Time       string `json:"time" jsonschema:"the start, HH:MM 24-hour, in the owner's time zone"`
}

type bookForGuestOut struct {
	BookingID  string    `json:"booking_id" jsonschema:"pass to get_booking, reschedule_booking or cancel_booking"`
	GuestName  string    `json:"guest_name"`
	Meeting    string    `json:"meeting"`
	When       mcpTimeAt `json:"when"`
	Ends       mcpTimeAt `json:"ends"`
	Where      string    `json:"where"`
	Invitation string    `json:"invitation" jsonschema:"whether and how the guest is being invited, in words"`
	ManageURL  string    `json:"manage_url,omitempty" jsonschema:"the booking's own page, where the guest can reschedule or cancel; for the owner to pass on if they want"`
}

func (h *Handler) mcpBookForGuest(ctx context.Context, _ *mcp.CallToolRequest, in bookForGuestIn) (*mcp.CallToolResult, bookForGuestOut, error) {
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, bookForGuestOut{}, err
	}
	name := strings.TrimSpace(in.GuestName)
	if name == "" || len(name) > 200 {
		return nil, bookForGuestOut{}, fmt.Errorf("guest_name is missing. Ask the owner who the meeting is with")
	}
	email, err := normalizeBookerEmail(strings.TrimSpace(in.GuestEmail))
	if err != nil {
		return nil, bookForGuestOut{}, fmt.Errorf("guest_email is missing or does not look like an email address. Without one, make a personal link instead (create_personal_link with a window exactly the meeting's length)")
	}
	et, slugs, err := h.offerType(ctx, o.UserID, in.EventType)
	if err != nil {
		return nil, bookForGuestOut{}, err
	}
	if et == nil {
		return nil, bookForGuestOut{}, fmt.Errorf("event_type must be one of the owner's meeting types: %s", strings.Join(slugs, ", "))
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(in.Date), o.Loc)
	minute, ok := parseClock(in.Time)
	if err != nil || !ok {
		return nil, bookForGuestOut{}, fmt.Errorf("date must be YYYY-MM-DD and time HH:MM, 24-hour, both in the owner's zone (%s)", o.zone())
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), minute/60, minute%60, 0, 0, o.Loc)
	if !start.After(time.Now()) {
		return nil, bookForGuestOut{}, fmt.Errorf("%s has already passed. Ask the owner for a later time", o.when(start))
	}

	b, err := h.createBookingForSlugWith(ctx, et.Slug, start,
		booking.Attendee{Name: name, Email: email, IANATimezone: o.zone()},
		nil, bookingExtras{OwnerInvited: true})
	var answerErr *answerError
	switch {
	case errors.Is(err, errCalendarClash), errors.Is(err, booking.ErrDoubleBooked):
		return nil, bookForGuestOut{}, fmt.Errorf("nothing was booked: the owner already has something at %s. Ask for another time, or offer a few with create_personal_link. Only the owner can book over their calendar, from the Bookmeet assistant on Telegram", o.when(start))
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		return nil, bookForGuestOut{}, fmt.Errorf("nothing was booked: %s is outside the times the owner takes meetings (their hours, notice or booking horizon). get_available_slots shows what is open. Only the owner can book outside their hours, from the Bookmeet assistant on Telegram", o.when(start))
	case errors.Is(err, errCalendarUnavailable):
		return nil, bookForGuestOut{}, fmt.Errorf("the owner's calendar could not be checked just now, so nothing was booked. Try again in a moment")
	case errors.As(err, &answerErr):
		return nil, bookForGuestOut{}, fmt.Errorf("%q has required questions for the guest, so it cannot be booked for them. Nothing was booked; send the guest the booking page instead", et.Name)
	case errors.Is(err, errPaymentRequired):
		return nil, bookForGuestOut{}, fmt.Errorf("%q takes payment, so the guest has to book it on the booking page. Nothing was booked", et.Name)
	case errors.Is(err, booking.ErrEmailThrottled):
		return nil, bookForGuestOut{}, fmt.Errorf("too many bookings for that email just now. Nothing was booked; try again later")
	case err != nil:
		return nil, bookForGuestOut{}, fmt.Errorf("book for guest: %w", err)
	}

	out := bookForGuestOut{
		BookingID: b.ID, GuestName: name, Meeting: et.Name,
		When:  mcpTimeAt{At: rfc3339OrEmpty(b.StartAt), Text: o.when(b.StartAt)},
		Ends:  mcpTimeAt{At: rfc3339OrEmpty(b.EndAt), Text: o.when(b.EndAt)},
		Where: locationWord(b.LocationType),
	}
	if b.PhysicalLocation != "" {
		out.Where = b.PhysicalLocation
	}
	if b.InviteDelivery == booking.InviteByNone {
		out.Invitation = "It is on the owner's calendar. This meeting type sends the guest no invitation, so the owner should tell them."
	} else {
		out.Invitation = "It is on the owner's calendar, and the guest's calendar invitation is on its way to " + email + "."
	}
	if tok, err := h.bookingSvc.IssueManageToken(ctx, b.ID); err == nil {
		out.ManageURL = strings.TrimRight(h.publicURL(), "/") + "/manage/" + tok
	} else {
		h.logger.ErrorContext(ctx, "mcp: manage link", "error", err)
	}
	return nil, out, nil
}

func (h *Handler) addAssistantBookTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "book_for_guest",
		Title: "Book a guest at one exact time",
		Description: "Book one exact time for a named guest whose email the owner gave, as the owner's own invitation: no link, no guest chat. " +
			"The guest gets the calendar invitation. Pass the date and time in the owner's own time zone. " +
			"Refuses a time outside the owner's hours or over something on their calendar, and says which. " +
			"Without the guest's email, use create_personal_link with a window exactly the meeting's length instead.",
		Annotations: toolWrites,
	}, h.mcpBookForGuest)
}

// ── answer_guest ─────────────────────────────────────────────────────────────

// answer_guest is the owner's buttons under the end-of-chat message on Telegram (tapHeld),
// for an agent. Booking a suggested time goes through bookPersonalLink with no waiver: it
// books only when the time is bookable under the owner's own rules. "Book anyway" over a
// clash or outside their hours stays the owner's tap, and the agent is told only which of
// the two stood in the way.

type answerGuestIn struct {
	LinkID string `json:"link_id"`
	HeldID int64  `json:"held_id" jsonschema:"from get_personal_link's left_for_owner"`
	Answer string `json:"answer" jsonschema:"book (one of the suggested times), none (none of the suggested times; the guest is not told), or handled (the owner will deal with it themselves)"`
	At     string `json:"at,omitempty" jsonschema:"for book: one of the suggested times, exactly as its 'at' field gives it"`
}

type answerGuestOut struct {
	LinkID string           `json:"link_id"`
	Result string           `json:"result" jsonschema:"what happened, in words"`
	Booked *bookForGuestOut `json:"booked,omitempty"`
}

func (h *Handler) mcpAnswerGuest(ctx context.Context, _ *mcp.CallToolRequest, in answerGuestIn) (*mcp.CallToolResult, answerGuestOut, error) {
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, answerGuestOut{}, err
	}
	links := h.personalLinks()
	held, err := links.HeldByID(ctx, o.UserID, in.HeldID)
	if err != nil || held.LinkID != strings.TrimSpace(in.LinkID) {
		return nil, answerGuestOut{}, fmt.Errorf("nothing with held_id %d on that link. Call get_personal_link to see what the guest left", in.HeldID)
	}
	if held.Outcome != "" {
		return nil, answerGuestOut{}, fmt.Errorf("that has already been answered (%s)", held.Outcome)
	}
	l, err := links.Get(ctx, o.UserID, held.LinkID)
	if err != nil {
		return nil, answerGuestOut{}, fmt.Errorf("that link is no longer available")
	}
	out := answerGuestOut{LinkID: l.ID}
	switch strings.TrimSpace(in.Answer) {
	case "none":
		if held.Kind != personallink.HeldSuggestions {
			return nil, answerGuestOut{}, fmt.Errorf("none answers suggested times; for this, use handled")
		}
		if links.ResolveHeld(ctx, held.ID, "none") != nil {
			return nil, answerGuestOut{}, fmt.Errorf("that has already been answered")
		}
		if l.Status == personallink.StatusProposed {
			_ = links.SetStatus(ctx, l.ID, personallink.StatusOpen)
		}
		out.Result = "None of the suggested times were booked. The guest has not been told, and the link still works for the times offered. The owner can reach out, or change the offer with create_personal_link and replaces_link_id."
		return nil, out, nil
	case "handled":
		if links.ResolveHeld(ctx, held.ID, "handled") != nil {
			return nil, answerGuestOut{}, fmt.Errorf("that has already been answered")
		}
		out.Result = "Marked as handled by the owner. The link stays as it is."
		return nil, out, nil
	case "book":
	default:
		return nil, answerGuestOut{}, fmt.Errorf("answer must be book, none or handled")
	}

	if held.Kind != personallink.HeldSuggestions {
		return nil, answerGuestOut{}, fmt.Errorf("only suggested times can be booked; for this, use handled")
	}
	var starts []string
	_ = json.Unmarshal([]byte(held.Content), &starts)
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(in.At))
	match := false
	for _, s := range starts {
		if t, e := time.Parse(time.RFC3339, s); e == nil && err == nil && t.Equal(at) {
			match = true
		}
	}
	if !match {
		return nil, answerGuestOut{}, fmt.Errorf("at must be one of the guest's suggested times, exactly as get_personal_link gives it")
	}
	organizer := booking.Attendee{Name: l.GuestName, Email: l.GuestEmail, IANATimezone: l.GuestTimezone, Locale: l.GuestLocale}
	b, err := h.bookPersonalLink(ctx, l, at, organizer, true)
	var answerErr *answerError
	switch {
	case err == nil:
	case errors.Is(err, personallink.ErrNotBookable):
		return nil, answerGuestOut{}, fmt.Errorf("that link has already been used or closed, so nothing was booked")
	case errors.Is(err, errCalendarClash), errors.Is(err, booking.ErrDoubleBooked):
		return nil, answerGuestOut{}, fmt.Errorf("nothing was booked: the owner already has something at %s. Only the owner can book over it, with Book anyway on Telegram; or answer none", o.when(at))
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		return nil, answerGuestOut{}, fmt.Errorf("nothing was booked: %s is outside the times the owner takes meetings. Only the owner can book it, with Book anyway on Telegram; or answer none", o.when(at))
	case errors.Is(err, errCalendarUnavailable):
		return nil, answerGuestOut{}, fmt.Errorf("the owner's calendar could not be checked just now, so nothing was booked. Try again in a moment")
	case errors.Is(err, booking.ErrBookingLimitReached):
		return nil, answerGuestOut{}, fmt.Errorf("that guest has reached a booking limit on this meeting type. Nothing was booked")
	case errors.Is(err, errPaymentRequired):
		return nil, answerGuestOut{}, fmt.Errorf("that meeting type takes payment, so it has to be booked on the booking page. Nothing was booked")
	case errors.As(err, &answerErr):
		return nil, answerGuestOut{}, fmt.Errorf("that meeting type has required questions, so it cannot be booked from here. Nothing was booked")
	default:
		return nil, answerGuestOut{}, fmt.Errorf("book suggested time: %w", err)
	}
	h.suggestedTimeBooked(ctx, held, l, at, b)
	booked := bookForGuestOut{
		BookingID: b.ID, GuestName: l.GuestName, Meeting: h.eventTypeNames(ctx, o.UserID)[l.EventTypeID],
		When: mcpTimeAt{At: rfc3339OrEmpty(b.StartAt), Text: o.when(b.StartAt)},
		Ends: mcpTimeAt{At: rfc3339OrEmpty(b.EndAt), Text: o.when(b.EndAt)}, Where: locationWord(b.LocationType),
	}
	if b.PhysicalLocation != "" {
		booked.Where = b.PhysicalLocation
	}
	if h.guestWillBeEmailed(ctx, b) {
		booked.Invitation = "The guest's calendar invitation is on its way to " + l.GuestEmail + "."
	} else {
		booked.Invitation = "No invitation email will reach the guest; the owner should tell them."
	}
	out.Result, out.Booked = "Booked "+l.GuestName+" for "+booked.When.Text+".", &booked
	return nil, out, nil
}

func (h *Handler) addAssistantAnswerTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "answer_guest",
		Title: "Answer what a guest left",
		Description: "Answer something a guest left for the owner on a personal link (see get_personal_link's left_for_owner): " +
			"book one of their suggested times, decline them all (none; the guest is not told), or mark a request or note as handled by the owner. " +
			"A suggested time books only if it is free under the owner's own rules; otherwise it is refused and only the owner can book it anyway. Ask the owner before answering.",
		Annotations: toolWrites,
	}, h.mcpAnswerGuest)
}
