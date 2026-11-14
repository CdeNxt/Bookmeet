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
	// A link that ran out longer ago than this is not announced: the owner connecting
	// Telegram late should not be met with a pile of old news.
	ranOutNewsFor = 3 * 24 * time.Hour
	// How long a details panel that was created, cancelled or abandoned is kept.
	draftKeptFor = 14 * 24 * time.Hour
	// How often, in sweeps, open links are counted for times left. Each count asks the
	// scheduling engine (and the owner's calendar), so this is slower than the chat sweep.
	emptyCheckEvery = 10
)

// StartPersonalLinkSweep watches for guest chats that have ended and tells their owners.
// A chat the guest ended in words is reported at once (plinkSession.end); this catches the
// ones that ended by the page closing or the guest going quiet.
func (h *Handler) StartPersonalLinkSweep(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(chatSweepEvery)
		defer ticker.Stop()
		for tick := 0; ; tick++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sweepChats(ctx)
				h.sweepRanOut(ctx)
				if tick%emptyCheckEvery == 0 {
					h.sweepEmptyLinks(ctx)
				}
				if tick%60 == 0 {
					h.tidyDrafts(ctx)
				}
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

// sweepRanOut tells owners about links that passed their expiry without a booking.
func (h *Handler) sweepRanOut(ctx context.Context) {
	ids, err := h.personalLinks().RanOut(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link sweep: ran out", "error", err)
		return
	}
	for _, id := range ids {
		h.tellRanOut(ctx, id)
	}
}

// tellRanOut sends the owner one message about a link that ran out unused: which meeting,
// how far the guest got, and a button to make a new one. Once per link.
func (h *Handler) tellRanOut(ctx context.Context, linkID string) {
	links := h.personalLinks()
	l, err := links.ByID(ctx, linkID) // reading it is what marks it expired
	if err != nil {
		return
	}
	if l.Status != personallink.StatusExpired || time.Since(l.ExpiresAt) > ranOutNewsFor || !h.assistantEnabled(ctx) {
		_, _ = links.MarkExpiryTold(ctx, l.ID) // nothing to announce
		return
	}
	bot, _ := h.getTelegram()
	var chatID int64
	if bot == nil || h.db.QueryRowContext(ctx, `SELECT chat_id FROM telegram_links WHERE user_id = ?`, l.UserID).Scan(&chatID) != nil {
		return // nowhere to send it yet; it goes out once there is, while it is still news
	}
	s, err := h.newOwnerSession(ctx, bot, l.UserID, chatID)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link sweep: session", "error", err)
		return
	}
	if first, err := links.MarkExpiryTold(ctx, l.ID); err != nil || !first {
		return
	}
	first := firstName(l.GuestName)
	var got string
	switch {
	case l.LastOpenedAt.IsZero():
		got = first + " never opened it."
	case l.GuestEmail == "":
		got = first + " opened it but did not give an email."
	case !l.DetailsDone:
		got = first + " gave an email (" + l.GuestEmail + ") and stopped there."
	default:
		got = first + " saw the times (email " + l.GuestEmail + ") but did not pick one."
	}
	kind := "meeting"
	if et := s.typeByID(l.EventTypeID); et != nil {
		kind = et.Name
	}
	text := fmt.Sprintf("%s's link ran out without a booking\n%s", l.GuestName, kind)
	if len(l.Windows) > 0 {
		a, z := l.Windows[0].Start.In(s.prof.Loc), l.Windows[len(l.Windows)-1].End.In(s.prof.Loc)
		text += " · offered " + a.Format("Mon 2 Jan")
		if a.Format("2006-01-02") != z.Format("2006-01-02") {
			text += " to " + z.Format("Mon 2 Jan")
		}
	}
	text += "\n\n" + got
	markup := telegram.Markup{{{Text: "New link for " + first, Data: "l|n|" + l.ID}}}
	if _, err := bot.Send(ctx, chatID, text, markup); err != nil {
		h.logger.WarnContext(ctx, "personal link sweep: tell owner", "error", err)
		return
	}
	s.remember(ctx, "event", fmt.Sprintf("%s's link (%s) ran out without a booking. %s", l.GuestName, kind, got))
}

// sweepEmptyLinks tells owners about open links that have nothing left to offer, before a
// guest finds that out: the calendar filled, or the times passed.
func (h *Handler) sweepEmptyLinks(ctx context.Context) {
	if !h.assistantEnabled(ctx) {
		return
	}
	links := h.personalLinks()
	ids, err := links.OpenUnbooked(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "personal link sweep: open links", "error", err)
		return
	}
	for _, id := range ids {
		l, err := links.ByID(ctx, id)
		if err != nil || !l.Active() {
			continue
		}
		res, err := h.personalLinkSlots(ctx, l, "UTC")
		if err != nil {
			continue // the calendar did not answer; look again next time
		}
		if len(res.Slots) == 0 {
			h.tellLinkEmpty(ctx, l, false)
		}
	}
}

// tellLinkEmpty tells the owner, once per link, that it has no open times left, with a
// button to offer new ones. guestThere says the guest is on the page now and has been asked
// for times that suit them; otherwise the sweep noticed first and the guest has not seen it.
func (h *Handler) tellLinkEmpty(ctx context.Context, l *personallink.Link, guestThere bool) {
	bot, _ := h.getTelegram()
	var chatID int64
	if bot == nil || h.db.QueryRowContext(ctx, `SELECT chat_id FROM telegram_links WHERE user_id = ?`, l.UserID).Scan(&chatID) != nil {
		return // nowhere to send it; the guest's side still works
	}
	links := h.personalLinks()
	if first, err := links.MarkEmptyTold(ctx, l.ID); err != nil || !first {
		return
	}
	s, err := h.newOwnerSession(ctx, bot, l.UserID, chatID)
	if err != nil {
		return
	}
	first := firstName(l.GuestName)
	kind := "meeting"
	if et := s.typeByID(l.EventTypeID); et != nil {
		kind = et.Name
	}
	text := fmt.Sprintf("%s's link has no open times left\n%s", l.GuestName, kind)
	if len(l.Windows) > 0 {
		a, z := l.Windows[0].Start.In(s.prof.Loc), l.Windows[len(l.Windows)-1].End.In(s.prof.Loc)
		text += " · offered " + a.Format("Mon 2 Jan")
		if a.Format("2006-01-02") != z.Format("2006-01-02") {
			text += " to " + z.Format("Mon 2 Jan")
		}
	}
	if guestThere {
		text += fmt.Sprintf("\n\n%s just opened it and found nothing to book. I've asked which times suit them; you'll get those as soon as they send them.", first)
	} else {
		text += fmt.Sprintf("\n\nEverything it offered is now busy or past. %s hasn't seen that yet.", first)
	}
	markup := telegram.Markup{{{Text: "New times for " + first, Data: "l|n|" + l.ID}}}
	if _, err := bot.Send(ctx, chatID, text, markup); err != nil {
		h.logger.WarnContext(ctx, "personal link: tell owner it is empty", "error", err)
		return
	}
	s.remember(ctx, "event", fmt.Sprintf("%s's link (%s) has no open times left.", l.GuestName, kind))
}

// tapNewLink starts a new link for the guest of one that ran out or has nothing left to
// offer. The owner is shown the old offer to change, or asked for new times; nothing is
// created until they tap Create.
func (s *ownerSession) tapNewLink(ctx context.Context, linkID string) string {
	l, err := s.h.personalLinks().Get(ctx, s.userID, linkID)
	if err != nil {
		return "That link is no longer there."
	}
	first := firstName(l.GuestName)
	res := s.toolChangeLink(ctx, l)
	switch {
	case strings.HasPrefix(res, "The panel is on screen"):
		s.say(ctx, "Here is the old offer. Tell me what to change, or adjust it with the buttons.")
	case strings.HasPrefix(res, "error"):
		return first + "'s link was used to book, so there is nothing to replace."
	default:
		s.say(ctx, fmt.Sprintf("Which days and times should I offer %s now?", first))
	}
	s.remember(ctx, "event", fmt.Sprintf("The owner asked for a new link for %s to replace their old one. When they give the days and times, call show_details with guest_name %q.", l.GuestName, l.GuestName))
	return ""
}

// tidyDrafts removes details panels that are finished with. An open one that has sat
// untouched that long is abandoned: its buttons would act on an offer nobody remembers.
func (h *Handler) tidyDrafts(ctx context.Context) {
	if _, err := h.db.ExecContext(ctx, `DELETE FROM assistant_drafts WHERE updated_at < ?`,
		time.Now().Add(-draftKeptFor).UTC().Format(time.RFC3339)); err != nil {
		h.logger.WarnContext(ctx, "assistant: tidy drafts", "error", err)
	}
}

// conflictsText names what is in the way of a time, for the owner: “Dentist” (1:30 PM to
// 2:30 PM). Empty when nothing could be named.
func conflictsText(v timeVerdict, loc *time.Location, clock func(time.Time) string) string {
	var parts []string
	for i, c := range v.Conflicts {
		if i == 3 {
			parts = append(parts, "more")
			break
		}
		parts = append(parts, fmt.Sprintf("“%s” (%s to %s)", c.Title, clock(c.Start.In(loc)), clock(c.End.In(loc))))
	}
	return strings.Join(parts, " and ")
}

// assistantExceptions reports whether the owner lets the assistant make exceptions to their
// availability at all. On, it still asks each time; off, the option is never offered.
func (h *Handler) assistantExceptions(ctx context.Context) bool {
	var on int
	if err := h.db.QueryRowContext(ctx, `SELECT assistant_exceptions FROM server_settings WHERE id = 1`).Scan(&on); err != nil {
		return true
	}
	return on != 0
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
	s, err := h.newPlinkSession(ctx, l, l.GuestTimezone, nil)
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
		switch {
		case l.GuestEmail == "":
			b.WriteString(first + " opened the link but gave no email.\n")
		case !l.EmptyToldAt.IsZero():
			b.WriteString(first + " gave an email; the link had no times left to offer.\n")
		default:
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
	// named holds the lines that carry calendar titles. They go to the owner and nowhere
	// else: not into the assistant's memory, which a model reads.
	var named strings.Builder
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
			when := o.Format("Mon 2 Jan") + ", " + plinkClock(o) + " " + o.Format("MST")
			v := h.suggestedTimeVerdict(ctx, l, t)
			label := when + v.note()
			if len(label) > 60 {
				label = label[:57] + "…"
			}
			markup = append(markup, []telegram.Button{{Text: label, Data: fmt.Sprintf("h|%d|s|%d", suggestions.ID, i)}})
			if what := conflictsText(v, s.ownerLoc, plinkClock); what != "" {
				fmt.Fprintf(&named, "%s clashes with %s.\n", when, what)
			}
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

	// A link with nothing left: the owner can also simply offer new times.
	if !booked && !l.EmptyToldAt.IsZero() {
		markup = append(markup, []telegram.Button{{Text: "New times for " + first, Data: "l|n|" + l.ID}})
	}

	text := b.String()
	if named.Len() > 0 {
		text += "\n" + named.String()
	}
	messageID, err := bot.Send(ctx, chatID, strings.TrimRight(text, "\n"), markup)
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
			if !s.exceptions {
				return "Exceptions are switched off in Settings."
			}
			return s.bookSuggested(ctx, held, l, start, true, messageID)
		}
		o := start.In(s.prof.Loc)
		when := o.Format("Mon 2 Jan") + " at " + s.clock(o.Hour()*60+o.Minute())
		v := s.h.suggestedTimeVerdict(ctx, l, start)
		if v.free() {
			return s.bookSuggested(ctx, held, l, start, false, 0)
		}
		// Say everything that is in the way, by name where it can be named, and ask.
		var why []string
		if v.Outside {
			why = append(why, when+" is outside the times you take meetings.")
		}
		clock := func(t time.Time) string { return s.clock(t.Hour()*60 + t.Minute()) }
		switch what := conflictsText(v, s.prof.Loc, clock); {
		case v.Clash && what != "":
			why = append(why, "It clashes with "+what+".")
		case v.Clash:
			why = append(why, "It clashes with something on your calendar.")
		case v.Unknown:
			why = append(why, "Your calendar could not be checked for that time.")
		}
		if !s.exceptions {
			s.say(ctx, strings.Join(why, " ")+" Exceptions to your availability are switched off in Settings → AI, so I can't book it.")
			return ""
		}
		ask := fmt.Sprintf("Book %s then anyway?", first)
		if v.Clash {
			ask = fmt.Sprintf("Book %s over it anyway? Both stay on your calendar; moving the other one is up to you.", first)
		}
		_, err = s.bot.Send(ctx, s.chatID, strings.Join(why, " ")+" "+ask,
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
	organizer := booking.Attendee{Name: l.GuestName, Email: l.GuestEmail, IANATimezone: l.GuestTimezone, Locale: l.GuestLocale}
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
	case errors.Is(err, booking.ErrDoubleBooked) && approved:
		s.say(ctx, "Another Bookmeet booking starts at exactly that time, and two can't start at the same moment. Nothing was booked.")
		return "Nothing booked."
	case errors.Is(err, booking.ErrDoubleBooked):
		return "Another booking has taken that time. Nothing was booked."
	case errors.Is(err, booking.ErrBookingLimitReached):
		s.say(ctx, "That guest has reached a booking limit on this meeting type. Nothing was booked.")
		return "Nothing booked."
	case errors.Is(err, errPaymentRequired):
		s.say(ctx, "That meeting type takes payment, so it has to be booked on the booking page. Nothing was booked.")
		return "Nothing booked."
	case errors.Is(err, errCalendarUnavailable):
		return "Your calendar could not be checked just now. Try again in a moment."
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		return "That time is no longer free. Nothing was booked."
	case errors.As(err, &answerErr):
		return "That meeting type has required questions, so it can't be booked from here."
	default:
		s.h.logger.ErrorContext(ctx, "assistant: book suggested time", "error", err)
		s.say(ctx, "The booking could not be made, and I don't have a clear reason. Nothing was booked. Try again in a moment.")
		return "Nothing booked."
	}
	links := s.h.personalLinks()
	_ = links.ResolveHeld(ctx, held.ID, "booked")
	if confirmMessageID != 0 {
		_ = s.bot.Edit(ctx, s.chatID, confirmMessageID, "Booked anyway.", nil)
	}
	// The guest is probably gone. They get the calendar invitation; the card is here for
	// when they reopen the link.
	if ps, err := s.h.newPlinkSession(ctx, l, l.GuestTimezone, nil); err == nil {
		emailed := s.h.guestWillBeEmailed(ctx, b)
		card := chatItem{Role: "assistant", Kind: "booked", Title: ps.t("confirmed", ps.ownerFirst()), Rows: ps.meetingRows(start)}
		if emailed {
			card.Rows = append(card.Rows, [2]string{ps.t("invite"), ps.t("invite_sent", l.GuestEmail)})
			card.Foot = ps.t("manage_hint")
		} else {
			card.Foot = ps.t("invite_none")
		}
		ps.add(ctx, card)
		s.h.notifyOwnerBooked(ctx, ps, b, emailed)
	}
	s.remember(ctx, "event", "The owner booked a time "+l.GuestName+" suggested.")
	return ""
}
