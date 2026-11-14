package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/slots"
)

// askFor has the "model" hand over one window on the given day and returns the panel.
func (f *ownerFixture) askFor(t *testing.T, day int, start, end string) string {
	t.Helper()
	f.model.calls("show_details", map[string]any{
		"guest_name": "Bill Mathers", "event_type": "intro",
		"windows": []map[string]string{{"date": date(day), "start": start, "end": end}},
	})
	f.message(t, "set it up")
	return f.panel()
}

func (f *ownerFixture) lastNotice() string {
	answers := f.tg.of("answerCallbackQuery")
	if len(answers) == 0 {
		return ""
	}
	text, _ := answers[len(answers)-1]["text"].(string)
	return text
}

// closeDay removes the owner's hours on the weekday `day` days from now.
func (f *ownerFixture) closeDay(t *testing.T, day int) {
	t.Helper()
	weekday := int(time.Now().UTC().AddDate(0, 0, day).Weekday())
	if _, err := f.h.db.Exec(`DELETE FROM availability_rules WHERE day_of_week = ?`, weekday); err != nil {
		t.Fatal(err)
	}
}

// The owner explicitly asks for a day they do not take meetings. The panel flags it and
// nothing is made until they confirm with the Allow tap; then the link offers its whole
// window, minus the one time the owner is busy for, and it books.
func TestOwnerAssistant_timesOutsideUsualHoursAreConfirmedFirst(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	day, _ := time.Parse("2006-01-02", date(3))
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: day.Add(10 * time.Hour), End: day.Add(10*time.Hour + 30*time.Minute)}}
	f.cal.mu.Unlock()

	panel := f.askFor(t, 3, "09:00", "11:00")
	if !strings.Contains(panel, "is outside the times you take meetings") || !strings.Contains(panel, "Tap Allow to confirm") {
		t.Fatalf("the panel does not flag the day and ask to confirm:\n%s", panel)
	}
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("a link was made for times outside usual hours without confirming (%d)", n)
	}
	if got := f.lastNotice(); !strings.Contains(got, "Tap Allow to confirm") {
		t.Errorf("the refusal said %q", got)
	}
	f.tap(t, "Allow outside my availability")
	if !strings.Contains(f.panel(), "Confirmed: this link offers those times too") {
		t.Fatalf("after Allow the panel says:\n%s", f.panel())
	}
	f.tap(t, "Create link")
	links := f.links(t)
	if len(links) != 1 {
		t.Fatalf("%d links after confirming; want 1", len(links))
	}
	res, err := f.h.personalLinkSlots(context.Background(), links[0], "UTC")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range res.Slots {
		st, _ := time.Parse(time.RFC3339, s.Start)
		got = append(got, st.UTC().Format("15:04"))
	}
	if want := []string{"09:00", "09:30", "10:30"}; !sameStrings(got, want) {
		t.Fatalf("the link offers %v; want %v (10:00 is busy)", got, want)
	}
	if _, err := f.h.bookPersonalLink(context.Background(), links[0], day.Add(9*time.Hour), lf.guest(), false); err != nil {
		t.Fatalf("booking through the link: %v", err)
	}
}

// workWeek gives the owner Monday to Friday, 9 to 5, and nothing at weekends.
func (f *ownerFixture) workWeek(t *testing.T) {
	t.Helper()
	if _, err := f.h.db.Exec(`DELETE FROM availability_rules WHERE user_id = ?`, f.userID); err != nil {
		t.Fatal(err)
	}
	for wd := 1; wd <= 5; wd++ {
		if _, err := f.h.db.Exec(`INSERT INTO availability_rules (id, user_id, day_of_week, start_time, end_time) VALUES (?, ?, ?, '09:00', '17:00')`,
			fmt.Sprintf("ww-%d", wd), f.userID, wd); err != nil {
			t.Fatal(err)
		}
	}
}

// Days given without times get the owner's usual hours, and the days they do not work are
// left out. Nothing needs confirming, and the model is told the usual hours.
func TestOwnerAssistant_daysWithoutTimesUseTheUsualHours(t *testing.T) {
	f := newOwnerFixture(t)
	f.workWeek(t)
	var ws []map[string]string
	weekdays := 0
	for d := 20; d < 27; d++ {
		ws = append(ws, map[string]string{"date": date(d)})
		if wd := time.Now().UTC().AddDate(0, 0, d).Weekday(); wd != time.Saturday && wd != time.Sunday {
			weekdays++
		}
	}
	f.model.calls("show_details", map[string]any{"guest_name": "Bill Mathers", "event_type": "intro", "windows": ws})
	f.message(t, "a link for Bill sometime that week")
	panel := f.panel()
	if got := strings.Count(panel, "9:00 AM to 5:00 PM"); got != weekdays {
		t.Fatalf("the panel offers %d days at 9 to 5; want the %d weekdays:\n%s", got, weekdays, panel)
	}
	if strings.Contains(panel, "Sat ") || strings.Contains(panel, "Sun ") || strings.Contains(panel, "Heads up") {
		t.Fatalf("a weekend day or a warning is on the panel:\n%s", panel)
	}
	f.model.mu.Lock()
	system := f.model.requests[len(f.model.requests)-1][0].Content
	f.model.mu.Unlock()
	if !strings.Contains(system, "Usual hours: Mon 09:00-17:00") || !strings.Contains(system, "Sat off") {
		t.Errorf("the model is not told the usual hours")
	}
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 1 {
		t.Fatalf("%d links; want 1", n)
	}
}

// Only days the owner does not work, without times: nothing is guessed. The model is told to
// ask the owner whether they want them anyway.
func TestOwnerAssistant_onlyDaysOffWithoutTimesAreAskedAbout(t *testing.T) {
	f := newOwnerFixture(t)
	f.workWeek(t)
	var sat string
	for d := 20; d < 27; d++ {
		if time.Now().UTC().AddDate(0, 0, d).Weekday() == time.Saturday {
			sat = date(d)
		}
	}
	f.model.calls("show_details", map[string]any{"guest_name": "Bill Mathers", "event_type": "intro",
		"windows": []map[string]string{{"date": sat}}})
	f.message(t, "a link for Bill on Saturday")
	f.model.mu.Lock()
	reqs := f.model.requests
	last := reqs[len(reqs)-1]
	f.model.mu.Unlock()
	if tool := last[len(last)-1].Content; !strings.Contains(tool, "none of those days are among the owner's usual working days") {
		t.Fatalf("the model was told %q", tool)
	}
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("%d links", n)
	}
}

// A direct booking is one exact time, so outside the owner's hours it still needs the Allow
// tap (that choice is kept on purpose: a link's windows are a range the owner drew).
func TestOwnerAssistant_aDirectBookingOutsideHoursStillNeedsTheTap(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	f.model.calls("show_details", map[string]any{
		"guest_name": "Bill Mathers", "guest_email": "bill@example.com", "event_type": "intro",
		"book_at": map[string]string{"date": date(3), "time": "09:00"},
	})
	f.message(t, "book it")
	if !strings.Contains(f.panel(), "outside the times you take meetings") || f.buttons()["Allow outside my availability"] == "" {
		t.Fatalf("a direct booking outside hours did not ask:\n%s", f.panel())
	}
	f.tap(t, "Book it")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("%d bookings without the Allow tap", n)
	}
}

// Nothing is created when the link would offer nothing: the owner is busy for every window.
func TestOwnerAssistant_aLinkWithNothingToOfferIsNotCreated(t *testing.T) {
	f := newOwnerFixture(t)
	day, _ := time.Parse("2006-01-02", date(3))
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: day.Add(8 * time.Hour), End: day.Add(12 * time.Hour)}}
	f.cal.mu.Unlock()
	panel := f.askFor(t, 3, "09:00", "11:00")
	if !strings.Contains(panel, "you're busy the whole time") {
		t.Fatalf("the panel does not name the busy day:\n%s", panel)
	}
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("%d links created with nothing to offer", n)
	}
	if !f.anyText("Nothing on that panel can be offered") {
		t.Errorf("the owner was not told why: %v", f.texts())
	}
	// The calendar clears; the same panel now makes the link.
	f.cal.mu.Lock()
	f.cal.busy = nil
	f.cal.mu.Unlock()
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 1 {
		t.Fatalf("%d links once the day was free; want 1", n)
	}
}

// How far ahead the owner takes bookings is a rule for the public booking page. A meeting the
// owner sets up is not held to it: a day beyond it shows no warning, and the link offers it.
func TestOwnerAssistant_datesBeyondTheBookingWindowNeedNoException(t *testing.T) {
	f := newOwnerFixture(t)
	if _, err := f.h.db.Exec(`UPDATE event_types SET max_future_days = 3 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	panel := f.askFor(t, 20, "09:00", "10:00")
	if strings.Contains(panel, "Heads up") || f.buttons()["Allow outside my availability"] != "" {
		t.Fatalf("a day beyond the booking window drew a warning:\n%s", panel)
	}
	f.tap(t, "Create link")
	links := f.links(t)
	if len(links) != 1 {
		t.Fatalf("%d links; want 1", len(links))
	}
	res, err := f.h.personalLinkSlots(context.Background(), links[0], "UTC")
	if err != nil || len(res.Slots) != 2 {
		t.Fatalf("the link offers %d times (err %v); want the two inside the window", len(res.Slots), err)
	}
}

// With exceptions switched off in Settings the assistant only reports: no Allow button, no
// link for times outside the owner's hours, and no "Book anyway" on a guest's suggestion.
func TestOwnerAssistant_withExceptionsOffItOnlyReports(t *testing.T) {
	f := newOwnerFixture(t)
	if rec := f.patch(t, `{"exceptions":false}`); rec.Code != http.StatusOK {
		t.Fatalf("switch exceptions off: %d - %s", rec.Code, rec.Body.String())
	}
	if f.settings(t)["exceptions"] != false {
		t.Fatal("settings do not report exceptions as off")
	}
	f.closeDay(t, 3)
	panel := f.askFor(t, 3, "09:00", "11:00")
	// The owner learns, before any guest could, which switch would let these times be offered.
	if !strings.Contains(panel, `turn on "Allow exceptions to my availability" in Settings → AI`) {
		t.Fatalf("the panel does not say which setting to turn on:\n%s", panel)
	}
	if _, ok := f.buttons()["Allow outside my availability"]; ok {
		t.Fatal("an Allow button is offered with exceptions off")
	}
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("%d links created with exceptions off", n)
	}
	if !f.anyText(`Nothing was made: those times are outside your availability`) || !f.anyText("Settings → AI") {
		t.Errorf("the refusal did not name the setting: %v", f.texts())
	}
}

// A link made while exceptions were off offers only the owner's usual hours. Turning the
// setting on is enough: the same link, already sent, offers its whole window.
func TestPersonalLink_turningExceptionsOnFixesALinkAlreadySent(t *testing.T) {
	f := newChatFixture(t)
	if rec := f.patch(t, `{"exceptions":false}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	weekday := int(f.day.Weekday())
	if _, err := f.h.db.Exec(`DELETE FROM availability_rules WHERE day_of_week = ?`, weekday); err != nil {
		t.Fatal(err)
	}
	if res, err := f.h.personalLinkSlots(context.Background(), f.link, "UTC"); err != nil || len(res.Slots) != 0 {
		t.Fatalf("with exceptions off the link offers %d times (err %v); want none on a closed day", len(res.Slots), err)
	}
	// Found empty, the owner is told why and which switch fixes it.
	before := f.ownerMessages()
	f.h.sweepEmptyLinks(context.Background())
	told := f.tg.sent(ownerChat)
	if len(told) != before+1 || !strings.Contains(told[len(told)-1], `Turn on "Allow exceptions to my availability" in Settings → AI`) {
		t.Fatalf("the owner was not told about the setting: %v", told[before:])
	}
	if rec := f.patch(t, `{"exceptions":true}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if res, err := f.h.personalLinkSlots(context.Background(), f.link, "UTC"); err != nil || len(res.Slots) == 0 {
		t.Fatalf("after turning exceptions on the link offers %d times (err %v)", len(res.Slots), err)
	}
}

func TestPersonalLinkChat_withExceptionsOffASuggestionOutsideHoursCannotBeBooked(t *testing.T) {
	f := newChatFixture(t)
	if rec := f.patch(t, `{"exceptions":false}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	f.throughDetails(t)
	weekday := int(f.day.AddDate(0, 0, 1).Weekday())
	if _, err := f.h.db.Exec(`DELETE FROM availability_rules WHERE day_of_week = ?`, weekday); err != nil {
		t.Fatal(err)
	}
	first, _ := f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})
	var label string
	for l := range f.buttons() {
		if strings.HasPrefix(l, first.Format("Mon 2 Jan")) {
			label = l
		}
	}
	f.tap(t, label)
	if !strings.Contains(f.lastText(), "switched off in Settings") {
		t.Fatalf("the owner was told:\n%s", f.lastText())
	}
	if _, ok := f.buttons()["Book anyway"]; ok {
		t.Error("Book anyway is offered with exceptions off")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Errorf("%d bookings", n)
	}
}

// The limit on how many bookings one guest may hold is there to stop guests over-booking
// themselves. It must not stop the owner inviting, or approving, that same guest.
func TestPersonalLinkChat_theGuestLimitDoesNotBlockTheOwnersOwnInvitation(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	if _, err := f.h.db.Exec(`UPDATE event_types SET max_active_bookings = 1 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	// Bill already holds the one booking the event type lets a guest make for themselves.
	if _, err := f.h.createBookingForSlug(ctx, "intro", f.at(20, 0), f.guestAttendee(), nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	if _, err := f.h.createBookingForSlug(ctx, "intro", f.at(21, 0), f.guestAttendee(), nil); err == nil {
		t.Fatal("the limit is not in force for an ordinary booking; the test proves nothing")
	}

	f.throughDetails(t)
	start := f.at(17, 0)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	if res := f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)}); res.Stage != "booked" {
		t.Fatalf("booking through the owner's own link was blocked by the guest limit: %v", res.Items)
	}
}

// When a suggested time lands on something in the owner's calendar, the owner is told what,
// by name. The name goes to the owner alone: not the guest, and not the assistant's memory,
// which a model reads.
func TestPersonalLinkChat_theOwnerIsToldWhatASuggestionClashesWithByName(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	first := f.day.AddDate(0, 0, 1).Add(21 * time.Hour)
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: first.Add(-30 * time.Minute), End: first.Add(30 * time.Minute)}}
	f.cal.titled = []calendar.BusyEvent{{Title: "Dentist", Start: first.Add(-30 * time.Minute), End: first.Add(30 * time.Minute)}}
	f.cal.mu.Unlock()
	f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})

	report := f.lastText()
	if !strings.Contains(report, "clashes with “Dentist” (8:30 PM to 9:30 PM)") {
		t.Fatalf("the owner's message does not name the clash:\n%s", report)
	}
	var label string
	for l := range f.buttons() {
		if strings.HasPrefix(l, first.Format("Mon 2 Jan")) {
			label = l
		}
	}
	if !strings.Contains(label, "clashes with Dentist") {
		t.Errorf("button = %q", label)
	}

	// Nowhere a guest or a model could read it.
	msgs, _ := f.h.personalLinks().Messages(context.Background(), f.link.ID)
	for _, m := range msgs {
		if strings.Contains(m.Content, "Dentist") {
			t.Errorf("the guest's conversation names the owner's event: %s", m.Content)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_messages WHERE content LIKE '%Dentist%'`); n != 0 {
		t.Errorf("the assistant's memory holds the event's name in %d entries", n)
	}

	f.tap(t, label)
	ask := f.lastText()
	if !strings.Contains(ask, "It clashes with “Dentist”") || !strings.Contains(ask, "Book Bill over it anyway?") {
		t.Fatalf("the question to the owner:\n%s", ask)
	}
	f.tap(t, "Book anyway")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after Book anyway; want 1", n)
	}
}

// Another Bookmeet meeting in the way is named too, and can be booked over on the owner's
// tap. Two bookings cannot start at the same instant, and the owner is told when that is why.
func TestPersonalLinkChat_theOwnerCanBookOverAnotherBookmeetMeeting(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	f.throughDetails(t)
	existing := f.day.AddDate(0, 0, 1).Add(20 * time.Hour)
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	kendall := lf.guest()
	kendall.Name = "Kendall Roy"
	if _, err := f.h.createBookingForSlug(ctx, "intro", existing, kendall, nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	overlapping, same := existing.Add(15*time.Minute), existing
	f.chat(t, f.token, plinkChatRequest{Action: "suggest"})
	f.chat(t, f.token, plinkChatRequest{Action: "suggest_send", Starts: []string{
		overlapping.Format(time.RFC3339), same.Format(time.RFC3339),
	}})
	f.chat(t, f.token, plinkChatRequest{Action: "end"})

	if !strings.Contains(f.lastText(), "clashes with “Intro with Kendall Roy”") {
		t.Fatalf("the owner's message does not name the other meeting:\n%s", f.lastText())
	}
	// The buttons under the end-of-chat message, kept by what they do: later messages bring
	// buttons of their own.
	taps := map[string]string{}
	for l, data := range f.buttons() {
		switch {
		case strings.Contains(l, "8:15 PM"):
			taps["overlapping"] = data
		case strings.Contains(l, "8:00 PM"):
			taps["same"] = data
		}
	}

	// The same start cannot be doubled, whatever the owner taps.
	f.tapFrom(t, ownerChat, taps["same"])
	f.tap(t, "Book anyway")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after trying the identical start; want only Kendall's", n)
	}
	if !strings.Contains(f.lastText(), "starts at exactly that time") {
		t.Errorf("the owner was told:\n%s", f.lastText())
	}
	if got := f.reload(t).Status; got != personallink.StatusProposed {
		t.Fatalf("link is %q after a booking that did not happen; want it still waiting", got)
	}

	// An overlapping start is the owner's call.
	if _, err := f.h.bookPersonalLink(ctx, f.reload(t), overlapping, f.guestAttendee(), true); err == nil {
		t.Fatal("an overlapping time was booked without the owner's say-so")
	}
	f.tapFrom(t, ownerChat, taps["overlapping"])
	f.tap(t, "Book anyway")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 2 {
		t.Fatalf("%d bookings after booking over Kendall's; want 2", n)
	}
}

// The date table the model reads is not the owner's booking window. It reaches months
// ahead, the model is told it is no limit, and a request five weeks out (past the three
// weeks the table once stopped at) reaches the panel.
func TestOwnerAssistant_datesWeeksAheadReachThePanel(t *testing.T) {
	f := newOwnerFixture(t)
	if _, err := f.h.db.Exec(`UPDATE event_types SET max_future_days = 14 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	panel := f.askFor(t, 35, "09:00", "10:00")
	if !strings.Contains(panel, "Create link") && f.buttons()["Create link"] == "" {
		t.Fatalf("a day five weeks out did not reach the panel:\n%s", panel)
	}

	f.model.mu.Lock()
	system := f.model.requests[len(f.model.requests)-1][0].Content
	f.model.mu.Unlock()
	for _, want := range []string{date(35), date(150), "says nothing about when the owner is free", "Never refuse"} {
		if !strings.Contains(system, want) {
			t.Errorf("the model's instructions are missing %q", want)
		}
	}
}

// A link already made for dates beyond the owner's booking window, without an exception,
// offers its times and books (the case that showed a guest "I'm having trouble").
func TestPersonalLinkChat_aLinkBeyondTheBookingWindowStillOffersAndBooks(t *testing.T) {
	f := newChatFixture(t)
	if _, err := f.h.db.Exec(`UPDATE event_types SET max_future_days = 1 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	res := f.throughDetails(t)
	last := res.Items[len(res.Items)-1]
	if last.Kind != "times" || len(labels(last)) == 0 {
		t.Fatalf("after the details: %v; want the times", kinds(res.Items))
	}
	start := f.at(17, 0)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	if res = f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)}); res.Stage != "booked" {
		t.Fatalf("confirming ended at %q (%v); want booked", res.Stage, kinds(res.Items))
	}
}
