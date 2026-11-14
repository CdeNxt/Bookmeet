package handler

import (
	"context"
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

// The owner asks for a day they do not take meetings. The panel says so before anything is
// made, nothing is created without the Allow tap, and with it the link really offers the times.
func TestOwnerAssistant_timesOutsideTheOwnersHoursNeedTheirSayAndThenWork(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	// Something already in the diary inside the window: an exception must not offer it.
	day, _ := time.Parse("2006-01-02", date(3))
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: day.Add(10 * time.Hour), End: day.Add(10*time.Hour + 30*time.Minute)}}
	f.cal.mu.Unlock()

	panel := f.askFor(t, 3, "09:00", "11:00")
	if !strings.Contains(panel, "Heads up:") || !strings.Contains(panel, "is outside the times you take meetings") {
		t.Fatalf("the panel does not warn that the day is outside the owner's hours:\n%s", panel)
	}
	if _, ok := f.buttons()["Allow outside my availability"]; !ok {
		t.Fatalf("buttons = %v; want an Allow button", f.buttons())
	}

	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("a link was created for times outside the owner's hours without their say (%d links)", n)
	}
	if got := f.lastNotice(); !strings.Contains(got, "outside your availability") {
		t.Errorf("the refusal said %q", got)
	}

	f.tap(t, "Allow outside my availability")
	if !strings.Contains(f.panel(), "Allowed: this link will offer those times anyway") {
		t.Fatalf("after Allow the panel says:\n%s", f.panel())
	}
	f.tap(t, "Create link")
	links := f.links(t)
	if len(links) != 1 || !links[0].OutsideAvailability {
		t.Fatalf("links = %+v; want one link carrying the exception", links)
	}

	// The guest is offered the times, minus the one the owner is busy for.
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
		t.Fatalf("the exception offers %v; want %v (10:00 is busy)", got, want)
	}
	// And one of them books, where the ordinary rules would refuse the whole day.
	if _, err := f.h.bookPersonalLink(context.Background(), links[0], day.Add(9*time.Hour), lf.guest(), false); err != nil {
		t.Fatalf("booking through the exception: %v", err)
	}
}

func TestOwnerAssistant_datesBeyondTheBookingWindowSayHowFar(t *testing.T) {
	f := newOwnerFixture(t)
	if _, err := f.h.db.Exec(`UPDATE event_types SET max_future_days = 14 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	panel := f.askFor(t, 20, "09:00", "10:00")
	if !strings.Contains(panel, "is beyond the 14 days ahead you take bookings") {
		t.Fatalf("the panel does not say the day is beyond the booking window:\n%s", panel)
	}
	f.tap(t, "Allow outside my availability")
	f.tap(t, "Create link")
	links := f.links(t)
	if len(links) != 1 {
		t.Fatalf("%d links; want 1", len(links))
	}
	res, err := f.h.personalLinkSlots(context.Background(), links[0], "UTC")
	if err != nil || len(res.Slots) != 2 {
		t.Fatalf("the exception offers %d times (err %v); want the two inside the window", len(res.Slots), err)
	}

	// A day inside the window needs no exception and shows no warning.
	if panel := f.askFor(t, 3, "09:00", "10:00"); strings.Contains(panel, "Heads up") {
		t.Errorf("a day inside the owner's availability drew a warning:\n%s", panel)
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
	if !strings.Contains(panel, "Exceptions to your availability are switched off") {
		t.Fatalf("panel:\n%s", panel)
	}
	if _, ok := f.buttons()["Allow outside my availability"]; ok {
		t.Fatal("an Allow button is offered with exceptions off")
	}
	f.tap(t, "Create link")
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("%d links created with exceptions off", n)
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
