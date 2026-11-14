package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/telegram"
)

// What the owner hears from a guest's chat, and what they can do about it.
//
// Two rules decide when:
//
//   - A booking reaches the owner the moment it happens (notifyOwnerBooked).
//   - Everything else the guest wanted passed on - times they suggested, a change they asked
//     for, a note, "I'll follow up", "I can't make it" - is held and sent as ONE message when
//     the chat is over: the guest said they were done, closed the page, or went quiet.
//
// And one rule decides what comes back: nothing. Whatever the owner taps here either books a
// time the guest suggested (the guest gets a calendar invitation) or changes the offer (the
// owner gets a new link to send). Nothing the owner does is relayed into the guest's chat.

const (
	chatQuietFor   = 5 * time.Minute  // no guest action for this long: the chat is over
	chatLeftFor    = 45 * time.Second // page closed for this long: the chat is over
	chatSweepEvery = time.Minute
)

// StartPersonalLinkSweep watches for guest chats that have ended and tells their owners.
// A chat the guest ended in words is reported at once (plinkSession.end); this catches the
// ones that ended by the page closing or the guest going quiet.
func (h *Handler) StartPersonalLinkSweep(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(chatSweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sweepChats(ctx)
			}
		}
	}()
}

func (h *Handler) sweepChats(ctx context.Context) {
	ids, err := h.personalLinks().ChatsOver(ctx, chatQuietFor, chatLeftFor)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link sweep", "error", err)
		return
	}
	for _, id := range ids {
		h.reportChat(ctx, id)
	}
}

func verdictNote(v string) string {
	switch v {
	case timeClash:
		return " · clashes with your calendar"
	case timeOutside:
		return " · outside your hours"
	case timeTaken:
		return " · taken by another booking"
	case timeUnknown:
		return " · calendar not checked"
	}
	return ""
}

// reportChat sends the owner one message about a guest's chat that is over: which meeting it
// is about, how it ended, and everything the guest wanted passed on. It does nothing when
// there is nothing new to say.
func (h *Handler) reportChat(ctx context.Context, linkID string) {
	links := h.personalLinks()
	l, err := links.ByID(ctx, linkID)
	if err != nil {
		return
	}
	all, err := links.HeldItems(ctx, l.ID)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link report: held", "error", err)
		return
	}
	var fresh []personallink.Held
	for _, item := range all {
		if item.ReportedAt.IsZero() {
			fresh = append(fresh, item)
		}
	}
	booked := l.Status == personallink.StatusBooked
	told := !l.ChatReportedAt.IsZero() && !l.ChatReportedAt.Before(l.ChatActiveAt)
	// Nothing held, and either the booking already told the owner everything, or the guest
	// never did anything, or this chat has been reported: say nothing.
	if len(fresh) == 0 && (booked || l.ChatActiveAt.IsZero() || told) {
		_ = links.MarkReported(ctx, l.ID)
		return
	}
	bot, _ := h.getTelegram()
	var chatID int64
	if bot == nil || h.db.QueryRowContext(ctx, `SELECT chat_id FROM telegram_links WHERE user_id = ?`, l.UserID).Scan(&chatID) != nil {
		return // nowhere to send it yet; the items stay held and go out once there is
	}
	s, err := h.newPlinkSession(ctx, l, l.GuestTimezone)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link report: session", "error", err)
		return
	}
	first := firstName(l.GuestName)

	var b strings.Builder
	if l.ChatMode == personallink.ChatEnded {
		b.WriteString(l.GuestName + " finished the chat\n")
	} else {
		b.WriteString(l.GuestName + " left the chat\n")
	}
	// Which meeting, briefly: the owner may have several with the same person.
	tag := s.etName + " · not booked"
	if booked {
		tag = s.etName + " · booked"
		var startAt string
		if h.db.QueryRowContext(ctx, `SELECT start_at FROM bookings WHERE id = ?`, l.BookingID).Scan(&startAt) == nil {
			if t, err := time.Parse(time.RFC3339, startAt); err == nil {
				o := t.In(s.ownerLoc)
				tag = s.etName + " · " + o.Format("Mon 2 Jan") + ", " + plinkClock(o)
			}
		}
	}
	b.WriteString(tag + "\n")
	if l.Topic != "" {
		b.WriteString("Topic: " + l.Topic + "\n")
	}
	if !booked {
		if l.GuestEmail == "" {
			b.WriteString(first + " opened the link but gave no email.\n")
		} else {
			b.WriteString(first + " gave an email and saw the times.\n")
		}
	}

	// The newest set of suggested times is the one that counts; an earlier set the guest
	// replaced is closed out without being shown.
	var suggestions *personallink.Held
	for i := range fresh {
		if fresh[i].Kind == personallink.HeldSuggestions {
			if suggestions != nil {
				_ = links.ResolveHeld(ctx, suggestions.ID, "superseded")
			}
			suggestions = &fresh[i]
		}
	}
	var markup telegram.Markup
	ids := make([]int64, 0, len(fresh))
	for _, item := range fresh {
		ids = append(ids, item.ID)
	}
	if suggestions != nil {
		var starts []string
		_ = json.Unmarshal([]byte(suggestions.Content), &starts)
		fmt.Fprintf(&b, "\n%s suggested other times. Tap one to book it and send the invitation:\n", first)
		for i, v := range starts {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				continue
			}
			o := t.In(s.ownerLoc)
			label := o.Format("Mon 2 Jan") + ", " + plinkClock(o) + " " + o.Format("MST") + verdictNote(h.suggestedTimeVerdict(ctx, l, t))
			markup = append(markup, []telegram.Button{{Text: label, Data: fmt.Sprintf("h|%d|s|%d", suggestions.ID, i)}})
		}
		markup = append(markup, []telegram.Button{{Text: "None of these", Data: fmt.Sprintf("h|%d|n", suggestions.ID)}})
	}
	labels := map[string]string{
		personallink.HeldChange:   "Asked for a change",
		personallink.HeldNote:     "Note for you",
		personallink.HeldFollowUp: "Will follow up with you",
		personallink.HeldCannot:   "Can't make it",
	}
	for _, item := range fresh {
		label, ok := labels[item.Kind]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n%s:\n“%s”\n", label, item.Content)
		if item.Kind == personallink.HeldChange {
			markup = append(markup, []telegram.Button{
				{Text: "Change the offer", Data: fmt.Sprintf("h|%d|c", item.ID)},
				{Text: "I'll handle it", Data: fmt.Sprintf("h|%d|i", item.ID)},
			})
		}
	}

	messageID, err := bot.Send(ctx, chatID, strings.TrimRight(b.String(), "\n"), markup)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link report: send", "error", err)
		return // not marked: the next sweep tries again
	}
	if err := links.MarkHeldReported(ctx, ids, messageID); err != nil {
		h.logger.WarnContext(ctx, "personal link report: mark held", "error", err)
	}
	_ = links.MarkReported(ctx, l.ID)
	if suggestions != nil && l.Active() {
		_ = links.SetStatus(ctx, l.ID, personallink.StatusProposed)
	}
	// So the owner's assistant knows what arrived if they ask about it.
	_, _ = h.db.ExecContext(ctx, `INSERT INTO assistant_messages (user_id, role, content, created_at) VALUES (?, 'event', ?, ?)`,
		l.UserID, "A guest's chat ended and the owner was sent this:\n"+b.String(), time.Now().UTC().Format(time.RFC3339))
}

// tapHeld acts on a button under an end-of-chat message. op is "s" (book this suggested
// time), "a" (book it anyway), "b" (leave it), "n" (none of these), "c" (change the offer)
// or "i" (I'll handle it). An item is answered once.
func (s *ownerSession) tapHeld(ctx context.Context, heldID int64, op, arg string, messageID int64) string {
	links := s.h.personalLinks()
	held, err := links.HeldByID(ctx, s.userID, heldID)
	if err != nil {
		return "That is no longer available."
	}
	l, err := links.Get(ctx, s.userID, held.LinkID)
	if err != nil {
		return "That link is no longer available."
	}
	first := firstName(l.GuestName)
	if op == "b" {
		_ = s.bot.Edit(ctx, s.chatID, messageID, "OK, not booked.", nil)
		return ""
	}
	if held.Outcome != "" {
		return "You've already answered this."
	}

	switch op {
	case "s", "a":
		var starts []string
		_ = json.Unmarshal([]byte(held.Content), &starts)
		i, err := strconv.Atoi(arg)
		if err != nil || i < 0 || i >= len(starts) {
			return ""
		}
		start, err := time.Parse(time.RFC3339, starts[i])
		if err != nil {
			return ""
		}
		if op == "a" {
			return s.bookSuggested(ctx, held, l, start, true, messageID)
		}
		o := start.In(s.prof.Loc)
		when := o.Format("Mon 2 Jan") + " at " + s.clock(o.Hour()*60+o.Minute())
		var why string
		switch s.h.suggestedTimeVerdict(ctx, l, start) {
		case timeFree:
			return s.bookSuggested(ctx, held, l, start, false, 0)
		case timeTaken:
			return "Another Bookmeet booking is at that time, so it can't be booked."
		case timeClash:
			why = "You already have something on your calendar on " + when + "."
		case timeOutside:
			why = when + " is outside the hours you take meetings."
		default:
			why = "Your calendar could not be checked for " + when + "."
		}
		_, err = s.bot.Send(ctx, s.chatID,
			fmt.Sprintf("%s Book %s then anyway? Moving anything else is up to you.", why, first),
			telegram.Markup{{
				{Text: "Book anyway", Data: fmt.Sprintf("h|%d|a|%d", held.ID, i)},
				{Text: "Leave it", Data: fmt.Sprintf("h|%d|b", held.ID)},
			}})
		if err != nil {
			s.h.logger.WarnContext(ctx, "assistant: ask book anyway", "error", err)
		}
	case "n":
		if links.ResolveHeld(ctx, held.ID, "none") != nil {
			return "You've already answered this."
		}
		if l.Status == personallink.StatusProposed {
			_ = links.SetStatus(ctx, l.ID, personallink.StatusOpen)
		}
		s.say(ctx, fmt.Sprintf("OK. %s hasn't been told anything, and the link still works for the times you offered. Reach out yourself, or tell me to change %s's offer and I'll make a new link.", first, first))
		s.remember(ctx, "event", "The owner chose none of "+l.GuestName+"'s suggested times. The guest was not told.")
	case "c":
		if links.ResolveHeld(ctx, held.ID, "change") != nil {
			return "You've already answered this."
		}
		if res := s.toolChangeLink(ctx, l); strings.HasPrefix(res, "error") || !strings.HasPrefix(res, "The panel is on screen") {
			s.say(ctx, fmt.Sprintf("Tell me what %s's new offer should be (the meeting type, days and times) and I'll make a new link.", first))
		} else {
			s.say(ctx, "Here is the current offer. Tell me what to change, or adjust it with the buttons.")
		}
		s.remember(ctx, "event", "The owner chose to change "+l.GuestName+"'s offer after their request: "+held.Content)
	case "i":
		if links.ResolveHeld(ctx, held.ID, "handled") != nil {
			return "You've already answered this."
		}
		s.say(ctx, fmt.Sprintf("OK, leaving it with you. %s's link stays as it is.", first))
	}
	return ""
}

// bookSuggested books a time the guest suggested, on the owner's tap. approved means the
// owner was told it clashes or is outside their hours and chose to book it anyway.
func (s *ownerSession) bookSuggested(ctx context.Context, held *personallink.Held, l *personallink.Link, start time.Time, approved bool, confirmMessageID int64) string {
	organizer := booking.Attendee{Name: l.GuestName, Email: l.GuestEmail, IANATimezone: l.GuestTimezone}
	var b *booking.Booking
	var err error
	if approved {
		b, err = s.h.bookPersonalLinkApproved(ctx, l, start, organizer)
	} else {
		b, err = s.h.bookPersonalLink(ctx, l, start, organizer, true)
	}
	var answerErr *answerError
	switch {
	case err == nil:
	case errors.Is(err, personallink.ErrNotBookable):
		return "That link has already been used or closed."
	case errors.Is(err, booking.ErrDoubleBooked):
		return "Another booking has taken that time. Nothing was booked."
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		return "That time is no longer free. Nothing was booked."
	case errors.As(err, &answerErr):
		return "That meeting type has required questions, so it can't be booked from here."
	default:
		s.h.logger.ErrorContext(ctx, "assistant: book suggested time", "error", err)
		return "The booking could not be made. Try again."
	}
	links := s.h.personalLinks()
	_ = links.ResolveHeld(ctx, held.ID, "booked")
	if confirmMessageID != 0 {
		_ = s.bot.Edit(ctx, s.chatID, confirmMessageID, "Booked anyway.", nil)
	}
	// The guest is probably gone. They get the calendar invitation; the card is here for
	// when they reopen the link.
	if ps, err := s.h.newPlinkSession(ctx, l, l.GuestTimezone); err == nil {
		emailed := s.h.guestWillBeEmailed(ctx, b)
		card := chatItem{Role: "assistant", Kind: "booked", Title: plinkT("confirmed", ps.ownerFirst()), Rows: ps.meetingRows(start)}
		if emailed {
			card.Rows = append(card.Rows, [2]string{plinkT("invite"), plinkT("invite_sent", l.GuestEmail)})
			card.Foot = plinkT("manage_hint")
		} else {
			card.Foot = plinkT("invite_none")
		}
		ps.add(ctx, card)
		s.h.notifyOwnerBooked(ctx, ps, b, emailed)
	}
	s.remember(ctx, "event", "The owner booked a time "+l.GuestName+" suggested.")
	return ""
}
