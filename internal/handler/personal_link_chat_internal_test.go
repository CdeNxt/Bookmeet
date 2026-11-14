package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/mailer"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/slots"
)

const guestZone = "America/Los_Angeles"

// chatFixture is an owner with the assistant on and one personal link for Bill, offering
// 17:00-18:30 UTC three days out. For a guest in Los Angeles that is a morning.
type chatFixture struct {
	*ownerFixture
	link  *personallink.Link
	token string
	day   time.Time // midnight UTC of the offered day
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	f := &chatFixture{ownerFixture: newOwnerFixture(t)}
	f.day, _ = time.Parse("2006-01-02", date(3))
	var err error
	f.link, f.token, err = f.h.personalLinks().Create(context.Background(), personallink.CreateParams{
		UserID: f.userID, EventTypeID: f.etID, GuestName: "Bill Mathers", Topic: "the contract",
		Windows: []personallink.Window{{Start: f.day.Add(17 * time.Hour), End: f.day.Add(18*time.Hour + 30*time.Minute)}},
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	return f
}

// at is hour:minute UTC on the offered day.
func (f *chatFixture) at(hour, minute int) time.Time {
	return f.day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// chat sends one step of the guest's conversation and returns what the page would draw.
func (f *chatFixture) chat(t *testing.T, token string, req plinkChatRequest) plinkChatResponse {
	t.Helper()
	if req.Timezone == "" {
		req.Timezone = guestZone
	}
	rec := f.chatRaw(token, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat %q: %d - %s", req.Action, rec.Code, rec.Body.String())
	}
	var out plinkChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *chatFixture) chatRaw(token string, req plinkChatRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/links/"+token+"/chat", strings.NewReader(string(body)))
	r.SetPathValue("token", token)
	rec := httptest.NewRecorder()
	f.h.PersonalLinkChat(rec, r)
	return rec
}

func (f *chatFixture) say(t *testing.T, text string) plinkChatResponse {
	t.Helper()
	return f.chat(t, f.token, plinkChatRequest{Action: "say", Text: text})
}

// throughDetails opens the link and gives an email and no phone, leaving the guest at the times.
func (f *chatFixture) throughDetails(t *testing.T) plinkChatResponse {
	t.Helper()
	f.chat(t, f.token, plinkChatRequest{Action: "open"})
	f.say(t, "bill@example.com")
	return f.chat(t, f.token, plinkChatRequest{Action: "skip_phone"})
}

func kinds(items []chatItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Role + ":" + it.Kind
	}
	return out
}

func labels(item chatItem) []string {
	var out []string
	for _, d := range item.Days {
		for _, s := range d.Slots {
			out = append(out, d.Label+" "+s.Label)
		}
	}
	return out
}

func (f *chatFixture) modelCalls() int {
	f.model.mu.Lock()
	defer f.model.mu.Unlock()
	return len(f.model.requests)
}

func (f *chatFixture) reload(t *testing.T) *personallink.Link {
	t.Helper()
	l, err := f.h.personalLinks().Get(context.Background(), f.userID, f.link.ID)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The opening message is the program's: who asked, how long, what kind, what it is about,
// and the request for an email. No times yet.
func TestPersonalLinkChat_opensWithTheProgramsGreetingAndNoTimes(t *testing.T) {
	f := newChatFixture(t)
	res := f.chat(t, f.token, plinkChatRequest{Action: "open"})

	if !res.History || res.Stage != "email" || len(res.Items) != 1 {
		t.Fatalf("open = history %v, stage %q, items %v; want one greeting at the email step", res.History, res.Stage, kinds(res.Items))
	}
	greeting := res.Items[0].Text
	for _, want := range []string{"Hi Bill, Jeroen asked me to set up a 30-minute meeting with you", "It’s about the contract.", "best email address"} {
		if !strings.Contains(greeting, want) {
			t.Errorf("greeting is missing %q:\n%s", want, greeting)
		}
	}
	if f.modelCalls() != 0 {
		t.Errorf("the greeting used the model %d times; it is the program's to write", f.modelCalls())
	}
	if f.reload(t).LastOpenedAt.IsZero() {
		t.Error("opening the link was not recorded")
	}

	// Closing the page and coming back continues the conversation; it does not restart it.
	again := f.chat(t, f.token, plinkChatRequest{Action: "open"})
	if len(again.Items) != 1 || again.Items[0].Text != greeting {
		t.Errorf("reopening = %v; want the same single greeting", kinds(again.Items))
	}
}

// Email, then phone, then times, in that order, with the program reading the details itself.
func TestPersonalLinkChat_detailsComeFirstAndTheTimesAreInTheGuestsZone(t *testing.T) {
	f := newChatFixture(t)
	f.chat(t, f.token, plinkChatRequest{Action: "open"})

	// Asking for a time before giving an email gets the question again, not a time.
	early := f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: f.at(17, 0).Format(time.RFC3339)})
	if early.Stage != "email" || len(early.Items) != 1 || early.Items[0].Kind != "text" {
		t.Fatalf("picking before the email = stage %q, %v", early.Stage, kinds(early.Items))
	}

	res := f.say(t, "sure, it's Bill@Example.com")
	if res.Stage != "phone" || len(res.Items) != 1 || !res.Items[0].Skip {
		t.Fatalf("after the email: stage %q, items %v; want the phone question with a Skip button", res.Stage, kinds(res.Items))
	}
	if got := f.reload(t).GuestEmail; !strings.EqualFold(got, "bill@example.com") {
		t.Errorf("stored email %q", got)
	}

	res = f.say(t, "+1 (555) 010-0142")
	if res.Stage != "times" || len(res.Items) != 1 || res.Items[0].Kind != "times" {
		t.Fatalf("after the phone: stage %q, items %v; want the times", res.Stage, kinds(res.Items))
	}
	if got := f.reload(t).GuestPhone; got != "+1 (555) 010-0142" {
		t.Errorf("stored phone %q", got)
	}
	// 17:00-18:30 UTC is 9:00-10:30 in Los Angeles in November.
	got := labels(res.Items[0])
	day := f.day.Format("Mon 2 Jan")
	want := []string{day + " 9:00 AM", day + " 9:30 AM", day + " 10:00 AM"}
	if !sameStrings(got, want) {
		t.Errorf("offered times\n got  %v\n want %v", got, want)
	}
	if head := res.Items[0].Head; !strings.Contains(head, "30 min") || !strings.Contains(head, "Los Angeles time (PST)") {
		t.Errorf("times heading %q; want the length and the guest's zone", head)
	}
	// The common path needs no model at all.
	if f.modelCalls() != 0 {
		t.Errorf("the details step used the model %d times", f.modelCalls())
	}
}

func TestPersonalLinkChat_pickConfirmBooksAndTellsTheOwnerAtOnce(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	start := f.at(17, 30)

	res := f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	if got := kinds(res.Items); len(got) != 2 || got[1] != "assistant:confirm" {
		t.Fatalf("after picking: %v; want the guest's choice and a confirm card", got)
	}
	// Both zones on the card: the guest's, and the owner's alongside.
	when := res.Items[1].Rows[0][1]
	if !strings.Contains(when, "9:30 AM PST") || !strings.Contains(when, "5:30 PM UTC for Jeroen") {
		t.Errorf("confirm card says %q; want the guest's time with the owner's alongside", when)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("picking a time booked it (%d bookings); confirming is the guest's second step", n)
	}

	sent := len(f.tg.sent(ownerChat))
	res = f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})
	if got := kinds(res.Items); res.Stage != "booked" || len(got) != 2 || got[0] != "assistant:booked" || got[1] != "assistant:text" {
		t.Fatalf("after confirming: stage %q, %v; want the booked card, then the closing line", res.Stage, got)
	}
	if invite := res.Items[0].Rows[len(res.Items[0].Rows)-1]; invite[1] != "Sent to bill@example.com" {
		t.Errorf("booked card's last row = %v; want the invite line", invite)
	}
	// The assistant finishes by asking if there is anything else and saying they can go.
	if !strings.Contains(res.Items[1].Text, "you can close this chat") || !res.Items[1].Done {
		t.Errorf("closing line = %q (done button %v)", res.Items[1].Text, res.Items[1].Done)
	}
	var bookedAt, tz string
	if err := f.h.db.QueryRow(`SELECT b.start_at, a.iana_timezone FROM bookings b JOIN booking_attendees a ON a.booking_id = b.id`).Scan(&bookedAt, &tz); err != nil {
		t.Fatalf("read booking: %v", err)
	}
	if got, _ := time.Parse(time.RFC3339, bookedAt); !got.Equal(start) || tz != guestZone {
		t.Errorf("booked %s in %s; want %s in %s", bookedAt, tz, start, guestZone)
	}
	if l := f.reload(t); l.Status != personallink.StatusBooked {
		t.Errorf("link is %q; want booked", l.Status)
	}

	// The owner hears the moment it happens, with the facts from the booking.
	told := f.tg.sent(ownerChat)
	if len(told) != sent+1 {
		t.Fatalf("the owner got %d messages; want 1", len(told)-sent)
	}
	for _, want := range []string{"Booked: Bill Mathers", "Topic: the contract", "5:30 PM to 6:00 PM UTC", "9:30 AM PST for Bill", "bill@example.com", "Phone: not given"} {
		if !strings.Contains(told[len(told)-1], want) {
			t.Errorf("the owner's message is missing %q:\n%s", want, told[len(told)-1])
		}
	}

	// The link is used: another confirm books nothing more.
	f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: f.at(18, 0).Format(time.RFC3339)})
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Errorf("%d bookings after a second confirm; want 1", n)
	}
}

func TestPersonalLinkChat_aTimeThatIsNotOfferedOrWasJustTakenBooksNothing(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)

	// Free on the owner's calendar, outside what the link offers.
	res := f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: f.at(20, 0).Format(time.RFC3339)})
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("a time outside the link's windows was booked")
	}
	if len(res.Items) != 1 || res.Items[0].Kind != "times" {
		t.Errorf("after an unoffered time: %v; want the real times again", kinds(res.Items))
	}

	// Offered when the guest picked it, taken by someone else before they confirmed.
	start := f.at(17, 30)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	if _, err := f.h.createBookingForSlug(context.Background(), "intro", start, lf.guest(), nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	res = f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings; want only the one that took the slot", n)
	}
	if l := f.reload(t); l.Status != personallink.StatusOpen {
		t.Errorf("link is %q after a booking that did not happen; want it still open", l.Status)
	}
	if len(res.Items) != 1 || res.Items[0].Kind != "times" || sameStrings(labels(res.Items[0]), nil) {
		t.Fatalf("after a taken time: %v; want the remaining times", kinds(res.Items))
	}
	for _, l := range labels(res.Items[0]) {
		if strings.HasSuffix(l, "9:30 AM") {
			t.Errorf("the taken time is still offered: %v", labels(res.Items[0]))
		}
	}
}

// What the guest types beyond their details goes to the model, which is told only what is on
// the link and the open times inside it, and can only end in something the program draws.
func TestPersonalLinkChat_theModelSeesOnlyTheLinkAndCanOnlyPickAnOfferedTime(t *testing.T) {
	f := newChatFixture(t)
	// Someone else's meeting on the owner's calendar that day, inside the link's window.
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	other := lf.guest()
	other.Name = "Kendall Roy"
	if _, err := f.h.createBookingForSlug(context.Background(), "intro", f.at(18, 0), other, nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	f.throughDetails(t)

	f.model.tool("pick_time", map[string]string{"date": f.day.Format("2006-01-02"), "time": "9:30 am"})
	res := f.say(t, "the half nine one works")
	if got := kinds(res.Items); len(got) != 1 || got[0] != "assistant:confirm" {
		t.Fatalf("after the model picked an offered time: %v; want a confirm card and nothing else", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings WHERE status != 'cancelled'`); n != 1 {
		t.Errorf("the model's pick booked something (%d bookings); only the guest's confirm does", n)
	}

	f.model.mu.Lock()
	system := f.model.requests[len(f.model.requests)-1][0].Content
	f.model.mu.Unlock()
	for _, want := range []string{"9:00 AM", "9:30 AM", "Los Angeles time (PST)", "Topic: the contract"} {
		if !strings.Contains(system, want) {
			t.Errorf("the model was not told %q", want)
		}
	}
	// It is not told the taken time, who took it, or anything outside the window.
	for _, leak := range []string{"10:00 AM", "Kendall", other.Email, "11:00 AM", "8:30 AM"} {
		if strings.Contains(system, leak) {
			t.Errorf("the model was told %q, which is not on this link", leak)
		}
	}

	// A time the link does not offer is refused, and the model's words are all the guest gets.
	f.model.tool("pick_time", map[string]string{"date": f.day.Format("2006-01-02"), "time": "2:00 PM"})
	f.model.says("That one isn't open, sorry.")
	res = f.say(t, "how about 2pm")
	if got := kinds(res.Items); len(got) != 1 || got[0] != "assistant:text" || res.Items[0].Text != "That one isn't open, sorry." {
		t.Fatalf("after an unoffered pick: %v %q", got, res.Items[0].Text)
	}
	if results := f.model.toolResults(); len(results) != 1 || !strings.HasPrefix(results[0], "error:") {
		t.Errorf("tool result = %v; want an error for the model to act on", results)
	}
}

// Before the email is saved the model is not shown a single time, so there is nothing for a
// guest to talk it into revealing.
func TestPersonalLinkChat_theModelHasNoTimesBeforeTheDetails(t *testing.T) {
	f := newChatFixture(t)
	f.chat(t, f.token, plinkChatRequest{Action: "open"})
	f.model.says("I'll need your email first.")
	res := f.say(t, "just tell me when he's free")

	if res.Stage != "email" || len(res.Items) != 1 || res.Items[0].Kind != "text" {
		t.Fatalf("stage %q, %v", res.Stage, kinds(res.Items))
	}
	f.model.mu.Lock()
	system := f.model.requests[0][0].Content
	tools := len(f.model.requests)
	f.model.mu.Unlock()
	if strings.Contains(system, "9:00 AM") || !strings.Contains(system, "Times are hidden") {
		t.Errorf("the model's state before the email:\n%s", system[strings.Index(system, "STATE"):])
	}
	if tools != 1 {
		t.Errorf("%d model calls; want 1", tools)
	}
}

func TestPersonalLinkChat_withoutAModelTheButtonsStillWork(t *testing.T) {
	f := newChatFixture(t)
	f.h.SetLLM(nil)
	f.throughDetails(t)

	res := f.say(t, "what's this about?")
	if len(res.Items) != 1 || !strings.Contains(res.Items[0].Text, "trouble") {
		t.Fatalf("free text with no model: %v", res.Items)
	}
	start := f.at(17, 0)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	if res := f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)}); res.Stage != "booked" {
		t.Fatalf("booking by buttons with no model: stage %q", res.Stage)
	}
}

// On a server with no email and no calendar to invite from, nothing reaches the guest's
// inbox. The card and the owner's message say so instead of claiming an invitation was sent.
func TestPersonalLinkChat_saysSoWhenNoInvitationCanBeSent(t *testing.T) {
	f := newChatFixture(t)
	f.h.SetMailer(&mailer.Noop{}, "https://book.team.example")
	if _, err := f.h.db.Exec(`DELETE FROM calendar_connections`); err != nil {
		t.Fatal(err)
	}
	f.throughDetails(t)
	start := f.at(17, 0)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	res := f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})

	if res.Stage != "booked" {
		t.Fatalf("stage %q; the booking itself should still succeed", res.Stage)
	}
	card := res.Items[0]
	for _, row := range card.Rows {
		if strings.Contains(row[1], "Sent to") {
			t.Errorf("the card claims an invitation was sent: %v", row)
		}
	}
	if !strings.Contains(card.Foot, "No email will be sent") {
		t.Errorf("card footer = %q; want it to say no email is coming", card.Foot)
	}
	told := f.tg.sent(ownerChat)
	if last := told[len(told)-1]; !strings.Contains(last, "no invitation went to Bill") {
		t.Errorf("the owner was not told the guest got no invitation:\n%s", last)
	}
}

// The page carries the owner's celebration effect, to play when the booking happens.
func TestPersonalLinkPage_carriesTheOwnersBookingEffect(t *testing.T) {
	f := newChatFixture(t)
	if _, err := f.h.db.Exec(`UPDATE server_settings SET booking_effect = 'confetti' WHERE id = 1`); err != nil {
		t.Skipf("booking effect is not stored where this test expects: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/i/"+f.token, nil)
	req.SetPathValue("token", f.token)
	rec := httptest.NewRecorder()
	f.h.PersonalLinkPage(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `id="bm-experience" data-booked="confetti"`) || !strings.Contains(body, "BookMeetEffects") {
		t.Error("the page does not carry the booking effect and the script that plays it")
	}
	if !strings.Contains(body, "bookmeet:booked") {
		t.Error("the page never fires the booked event")
	}
}

// The module's switch governs the links too.
func TestPersonalLinkChat_switchedOffTheLinkDoesNotOpen(t *testing.T) {
	f := newChatFixture(t)
	if rec := f.patch(t, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("switch off: %d", rec.Code)
	}
	res := f.chat(t, f.token, plinkChatRequest{Action: "open"})
	if res.Stage != "closed" || len(res.Items) != 1 || !strings.Contains(res.Items[0].Text, "no longer active") {
		t.Fatalf("stage %q, %v", res.Stage, res.Items)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM personal_link_messages`); n != 0 {
		t.Errorf("a conversation was started on a switched-off link")
	}
}

// A link that replaced an earlier one for the same guest confirms their details instead of
// asking again, and goes straight to the times.
func TestPersonalLinkChat_aReplacementLinkDoesNotAskForDetailsAgain(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	_, fresh, err := f.h.personalLinks().Replace(context.Background(), f.userID, f.link.ID, personallink.CreateParams{
		EventTypeID: f.etID, GuestName: "Bill Mathers",
		Windows: []personallink.Window{{Start: f.at(19, 0), End: f.at(20, 0)}},
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	res := f.chat(t, fresh, plinkChatRequest{Action: "open"})
	if res.Stage != "times" || len(res.Items) != 2 || res.Items[1].Kind != "times" {
		t.Fatalf("new link opens at stage %q with %v; want a greeting and the times", res.Stage, kinds(res.Items))
	}
	if !strings.Contains(res.Items[0].Text, "I still have your email as bill@example.com") {
		t.Errorf("greeting on the new link:\n%s", res.Items[0].Text)
	}
	// The old link is finished.
	old := f.chat(t, f.token, plinkChatRequest{Action: "say", Text: "hello?"})
	if old.Stage != "closed" {
		t.Errorf("the replaced link is at stage %q; want closed", old.Stage)
	}
}

func TestPersonalLinkPage_isAPrivateShellForARealLinkOnly(t *testing.T) {
	f := newChatFixture(t)
	get := func(tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/i/"+tok, nil)
		req.SetPathValue("token", tok)
		rec := httptest.NewRecorder()
		f.h.PersonalLinkPage(rec, req)
		return rec
	}
	if rec := get("not-a-real-token"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: %d; want 404", rec.Code)
	}
	if rec := f.chatRaw("not-a-real-token", plinkChatRequest{Action: "open"}); rec.Code != http.StatusNotFound {
		t.Errorf("chat on an unknown token: %d; want 404", rec.Code)
	}

	rec := get(f.token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Booking with Jeroen") {
		t.Fatalf("real token: %d", rec.Code)
	}
	// Addressed to one person: not indexed, and the token does not leak in a Referer.
	if rec.Header().Get("X-Robots-Tag") == "" || rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("privacy headers = %v", rec.Header())
	}
	// The page is a shell: nothing about the guest or the offer is in it until the chat loads.
	if strings.Contains(rec.Body.String(), "Bill") || strings.Contains(rec.Body.String(), "contract") {
		t.Error("the page's HTML carries the guest's details")
	}
}

// ---- suggesting other times, things to pass on, and the end of the chat ----

func (f *chatFixture) held(t *testing.T) []personallink.Held {
	t.Helper()
	items, err := f.h.personalLinks().HeldItems(context.Background(), f.link.ID)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (f *chatFixture) ownerMessages() int { return len(f.tg.sent(ownerChat)) }

// suggest takes the guest through naming two other times and sending them: 1:00 PM the day
// after the offer and 10:00 AM the day after that, Los Angeles time.
func (f *chatFixture) suggest(t *testing.T) (first, second time.Time) {
	t.Helper()
	first = f.day.AddDate(0, 0, 1).Add(21 * time.Hour)  // 1:00 PM PST
	second = f.day.AddDate(0, 0, 2).Add(18 * time.Hour) // 10:00 AM PST
	f.chat(t, f.token, plinkChatRequest{Action: "suggest"})
	f.model.tool("suggest_times", map[string]any{"suggestions": []map[string]string{
		{"date": f.day.AddDate(0, 0, 1).Format("2006-01-02"), "time": "1:00 PM"},
		{"date": f.day.AddDate(0, 0, 2).Format("2006-01-02"), "time": "10:00 AM"},
	}})
	card := f.say(t, "the day after at 1, or the one after that at 10")
	if got := kinds(card.Items); len(got) != 1 || got[0] != "assistant:suggest" {
		t.Fatalf("after naming times: %v; want a card to confirm", got)
	}
	f.chat(t, f.token, plinkChatRequest{Action: "suggest_send", Starts: card.Items[0].Starts})
	return first, second
}

// The guest names other times, sees them back in both zones, and sends them. Nothing reaches
// the owner until the chat is over, and then it is one message with a button per time.
func TestPersonalLinkChat_suggestedTimesAreHeldUntilTheChatEnds(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	before := f.ownerMessages()

	asked := f.chat(t, f.token, plinkChatRequest{Action: "suggest"})
	if got := kinds(asked.Items); len(got) != 2 || !strings.Contains(asked.Items[1].Text, "up to three") {
		t.Fatalf("after Suggest another time: %v", got)
	}
	f.model.tool("suggest_times", map[string]any{"suggestions": []map[string]string{
		{"date": f.day.AddDate(0, 0, 1).Format("2006-01-02"), "time": "1:00 PM"},
		{"date": f.day.AddDate(0, 0, 2).Format("2006-01-02"), "time": "10:00 AM"},
	}})
	card := f.say(t, "the day after at 1, or the one after that at 10").Items[0]
	if len(card.Rows) != 2 || card.Label != "Send to Jeroen" || len(card.Starts) != 2 {
		t.Fatalf("suggest card = %+v", card)
	}
	if got := card.Rows[0][1]; !strings.Contains(got, "1:00 PM PST") || !strings.Contains(got, "9:00 PM UTC for Jeroen") {
		t.Errorf("suggest card time %q; want both zones", got)
	}
	if len(f.held(t)) != 0 {
		t.Fatal("the suggestion was held before the guest confirmed it")
	}

	sent := f.chat(t, f.token, plinkChatRequest{Action: "suggest_send", Starts: card.Starts})
	if got := kinds(sent.Items); len(got) != 2 || !sent.Items[1].Done {
		t.Fatalf("after sending: %v; want the acknowledgement and the closing question", got)
	}
	if !strings.Contains(sent.Items[0].Text, "If one works you’ll get an invitation at bill@example.com") {
		t.Errorf("acknowledgement = %q", sent.Items[0].Text)
	}
	if got := f.ownerMessages(); got != before {
		t.Fatalf("the owner was messaged %d times while the guest was still in the chat", got-before)
	}

	ended := f.chat(t, f.token, plinkChatRequest{Action: "end"})
	if !ended.Ended || !strings.Contains(ended.Items[len(ended.Items)-1].Text, "Thanks, Bill") {
		t.Fatalf("after That's all: ended %v, %v", ended.Ended, ended.Items)
	}
	if got := f.ownerMessages(); got != before+1 {
		t.Fatalf("the owner got %d messages when the chat ended; want 1", got-before)
	}
	report := f.tg.sent(ownerChat)[before]
	for _, want := range []string{"Bill Mathers finished the chat", "Intro · not booked", "Topic: the contract", "Bill suggested other times"} {
		if !strings.Contains(report, want) {
			t.Errorf("the owner's message is missing %q:\n%s", want, report)
		}
	}
	if got := f.buttons(); len(got) != 3 {
		t.Errorf("buttons = %v; want one per suggested time and None of these", got)
	}
	if got := f.reload(t).Status; got != personallink.StatusProposed {
		t.Errorf("link is %q; want proposed while the owner decides", got)
	}
}

// Every suggestion reaches the owner, including ones they could not normally take, marked
// as such. The guest is never told which.
func TestPersonalLinkChat_theOwnerSeesWhichSuggestionsClashAndTheGuestDoesNot(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	// The owner takes no meetings on the weekday of the second suggestion.
	weekday := int(f.day.AddDate(0, 0, 2).Weekday())
	if _, err := f.h.db.Exec(`DELETE FROM availability_rules WHERE day_of_week = ?`, weekday); err != nil {
		t.Fatal(err)
	}
	first, second := f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})

	var free, outside string
	for label := range f.buttons() {
		switch {
		case strings.HasPrefix(label, first.Format("Mon 2 Jan")):
			free = label
		case strings.HasPrefix(label, second.Format("Mon 2 Jan")):
			outside = label
		}
	}
	if free == "" || strings.Contains(free, "·") {
		t.Errorf("the free suggestion's button = %q; want it unmarked", free)
	}
	if !strings.Contains(outside, "outside your hours") {
		t.Errorf("the other suggestion's button = %q; want it marked as outside the owner's hours", outside)
	}
	msgs, _ := f.h.personalLinks().Messages(context.Background(), f.link.ID)
	for _, m := range msgs {
		for _, leak := range []string{"outside your hours", "clash", "taken"} {
			if strings.Contains(m.Content, leak) {
				t.Errorf("the guest's conversation mentions %q: %s", leak, m.Content)
			}
		}
	}

	// Tapping the marked one asks first; "Book anyway" books it over the owner's own rules.
	f.tap(t, outside)
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("a time outside the owner's hours was booked on the first tap")
	}
	if !strings.Contains(f.lastText(), "outside the hours you take meetings") {
		t.Fatalf("the owner was not asked before booking it:\n%s", f.lastText())
	}
	f.tap(t, "Book anyway")
	var startAt string
	if err := f.h.db.QueryRow(`SELECT start_at FROM bookings`).Scan(&startAt); err != nil {
		t.Fatalf("no booking after Book anyway: %v", err)
	}
	if got, _ := time.Parse(time.RFC3339, startAt); !got.Equal(second) {
		t.Errorf("booked %s; want %s", startAt, second)
	}
	if got := f.reload(t).Status; got != personallink.StatusBooked {
		t.Errorf("link is %q; want booked", got)
	}
}

func TestPersonalLinkChat_tappingAFreeSuggestionBooksItOnce(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	first, _ := f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})
	var label string
	for l := range f.buttons() {
		if strings.HasPrefix(l, first.Format("Mon 2 Jan")) {
			label = l
		}
	}
	data := f.buttons()[label]

	f.tap(t, label)
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after tapping a free suggestion; want 1", n)
	}
	if !strings.Contains(f.lastText(), "Booked: Bill Mathers") {
		t.Errorf("the owner's confirmation:\n%s", f.lastText())
	}
	// The guest is gone; the card is waiting for when they reopen the link.
	back := f.chat(t, f.token, plinkChatRequest{Action: "open"})
	last := back.Items[len(back.Items)-1]
	if back.Stage != "booked" || last.Kind != "booked" || last.Title != "Jeroen confirmed. You’re booked." {
		t.Errorf("reopening the link: stage %q, last item %+v", back.Stage, last)
	}

	// The same button again, or the other suggestion: answered already, nothing more booked.
	f.tapFrom(t, ownerChat, data)
	f.tap(t, "None of these")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Errorf("%d bookings after tapping again", n)
	}
	answers := f.tg.of("answerCallbackQuery")
	if last := answers[len(answers)-1]; last["text"] != "You've already answered this." {
		t.Errorf("a second answer got %v", last)
	}
}

// "None of these" tells the guest nothing: the owner follows up themselves.
func TestPersonalLinkChat_noneOfTheseSendsTheGuestNothing(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})
	before, _ := f.h.personalLinks().Messages(context.Background(), f.link.ID)

	f.tap(t, "None of these")
	after, _ := f.h.personalLinks().Messages(context.Background(), f.link.ID)
	if len(after) != len(before) {
		t.Errorf("the guest's conversation grew by %d after None of these", len(after)-len(before))
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Errorf("None of these booked something")
	}
	if got := f.reload(t).Status; got != personallink.StatusOpen {
		t.Errorf("link is %q; want open again for the original times", got)
	}
	if !strings.Contains(f.lastText(), "hasn't been told anything") {
		t.Errorf("the owner's reply:\n%s", f.lastText())
	}
}

// A booking reaches the owner at once; a note the guest leaves comes afterwards, in its own
// single message that says which meeting it is about and quotes the guest exactly.
func TestPersonalLinkChat_aNoteIsHeldAndNamesTheMeeting(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	before := f.ownerMessages()

	// The model retells the note in its own words; what is kept is what the guest typed.
	f.model.tool("notify_owner", map[string]string{"kind": "note", "guest_words": "Guest will bring a contract"})
	res := f.say(t, "Tell Jason I'll bring the contract")
	if len(res.Items) != 1 || res.Items[0].Text != "I’ll pass that on to Jeroen." {
		t.Fatalf("reply to a note = %v", res.Items)
	}
	if held := f.held(t); len(held) != 1 || held[0].Kind != personallink.HeldNote || held[0].Content != "Tell Jason I'll bring the contract" {
		t.Fatalf("held = %+v; want the guest's own words", held)
	}
	if f.ownerMessages() != before {
		t.Fatal("the note reached the owner while the chat was still going")
	}

	start := f.at(17, 30)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})
	if got := f.ownerMessages(); got != before+1 {
		t.Fatalf("after booking the owner has %d new messages; want the booking alone", got-before)
	}

	f.chat(t, f.token, plinkChatRequest{Action: "end"})
	if got := f.ownerMessages(); got != before+2 {
		t.Fatalf("after the chat ended the owner has %d new messages; want 2", got-before)
	}
	report := f.tg.sent(ownerChat)[before+1]
	tag := "Intro · " + start.Format("Mon 2 Jan") + ", 5:30 PM"
	for _, want := range []string{"Bill Mathers finished the chat", tag, "Note for you:", "“Tell Jason I'll bring the contract”"} {
		if !strings.Contains(report, want) {
			t.Errorf("the end-of-chat message is missing %q:\n%s", want, report)
		}
	}
}

// A booking with nothing more to pass on is one message, not two.
func TestPersonalLinkChat_aBookingWithNothingElseIsOneMessage(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	before := f.ownerMessages()
	start := f.at(17, 0)
	f.chat(t, f.token, plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	f.chat(t, f.token, plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})
	// With no model, a plain "no" to the closing question is read by the program.
	f.h.SetLLM(nil)
	res := f.say(t, "No thanks")
	if !res.Ended {
		t.Fatalf("a plain no did not end the chat: %v", res.Items)
	}
	f.h.sweepChats(context.Background())
	if got := f.ownerMessages(); got != before+1 {
		t.Errorf("the owner got %d messages for a booking with nothing else; want 1", got-before)
	}
}

// A request to change the meeting is the owner's to act on: a new link, or handle it.
func TestPersonalLinkChat_aChangeRequestGoesToTheOwnerWithTheirTwoChoices(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	f.model.tool("notify_owner", map[string]string{"kind": "change_request", "guest_words": "Let's make this in person"})
	res := f.say(t, "Let's make this in person")
	if got := kinds(res.Items); len(got) != 2 || res.Items[0].Text != "I’ll pass that to Jeroen, who will be in touch." || !res.Items[1].Done {
		t.Fatalf("reply to a change request = %v %v", got, res.Items)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM event_types WHERE allow_in_person = 1`); n != 0 {
		t.Fatal("the guest's request changed the meeting")
	}
	f.chat(t, f.token, plinkChatRequest{Action: "end"})

	if !strings.Contains(f.lastText(), "Asked for a change:\n“Let's make this in person”") {
		t.Fatalf("the owner's message:\n%s", f.lastText())
	}
	if _, ok := f.buttons()["I'll handle it"]; !ok {
		t.Fatalf("buttons = %v", f.buttons())
	}
	f.tap(t, "Change the offer")
	if !strings.Contains(f.panel(), "Change the offer") || !strings.Contains(f.panel(), "Guest: Bill Mathers") {
		t.Errorf("Change the offer did not open the panel for Bill:\n%s", f.panel())
	}
}

// A guest who closes the page, or goes quiet, never says they are done. The sweep notices
// and tells the owner once.
func TestPersonalLinkChat_leavingOrGoingQuietEndsTheChatToo(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	f.model.tool("notify_owner", map[string]string{"kind": "follow_up", "guest_words": "x"})
	f.say(t, "I'll circle back with Jason on a different option")
	before := f.ownerMessages()
	ctx := context.Background()

	// Still here, and not quiet for long: nothing yet.
	f.h.sweepChats(ctx)
	if f.ownerMessages() != before {
		t.Fatal("the owner was told while the guest was still in the chat")
	}

	// The page closes.
	if rec := f.chatRaw(f.token, plinkChatRequest{Action: "leave"}); rec.Code != http.StatusNoContent {
		t.Fatalf("leave: %d", rec.Code)
	}
	f.h.sweepChats(ctx)
	if f.ownerMessages() != before {
		t.Fatal("the owner was told the instant the page closed; a reload would look the same")
	}
	// ...and stays closed.
	// (Their last action three minutes ago, the page closed two minutes ago.)
	if _, err := f.h.db.Exec(`UPDATE personal_links SET chat_active_at = ?, chat_left_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-3*time.Minute).Format(time.RFC3339),
		time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339), f.link.ID); err != nil {
		t.Fatal(err)
	}
	f.h.sweepChats(ctx)
	if got := f.ownerMessages(); got != before+1 {
		t.Fatalf("after the page stayed closed the owner has %d new messages; want 1", got-before)
	}
	report := f.tg.sent(ownerChat)[before]
	for _, want := range []string{"Bill Mathers left the chat", "Will follow up with you:", "“I'll circle back with Jason on a different option”"} {
		if !strings.Contains(report, want) {
			t.Errorf("the owner's message is missing %q:\n%s", want, report)
		}
	}
	if got := f.reload(t).Status; got != personallink.StatusFollowUp {
		t.Errorf("link is %q; want followup, still open", got)
	}

	// Told once: another sweep, even much later, says nothing more.
	if _, err := f.h.db.Exec(`UPDATE personal_links SET chat_active_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), f.link.ID); err != nil {
		t.Fatal(err)
	}
	f.h.sweepChats(ctx)
	if got := f.ownerMessages(); got != before+1 {
		t.Errorf("a later sweep sent %d more messages", got-before-1)
	}
}

// A suggestion that lands on something in the owner's calendar is marked as a clash, and
// booking it over that is the one thing only the owner's "Book anyway" can do.
func TestPersonalLinkChat_aCalendarClashIsMarkedAndOnlyTheOwnerCanBookOverIt(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	first := f.day.AddDate(0, 0, 1).Add(21 * time.Hour) // the first time suggest() names
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: first.Add(-15 * time.Minute), End: first.Add(45 * time.Minute)}}
	f.cal.mu.Unlock()
	f.suggest(t)
	f.chat(t, f.token, plinkChatRequest{Action: "end"})

	var clash string
	for label := range f.buttons() {
		if strings.HasPrefix(label, first.Format("Mon 2 Jan")) {
			clash = label
		}
	}
	if !strings.Contains(clash, "clashes with your calendar") {
		t.Fatalf("button = %q; want the calendar clash marked", clash)
	}

	// The ordinary path refuses a time the calendar has taken, for a guest and for an agent.
	if _, err := f.h.bookPersonalLink(context.Background(), f.reload(t), first, f.guestAttendee(), true); err == nil {
		t.Fatal("a time the owner's calendar has taken was booked without the owner's say-so")
	}

	f.tap(t, clash)
	if !strings.Contains(f.lastText(), "You already have something on your calendar") {
		t.Fatalf("the owner was not warned:\n%s", f.lastText())
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatal("it was booked before the owner confirmed")
	}
	f.tap(t, "Book anyway")
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after Book anyway; want 1", n)
	}
}

func (f *chatFixture) guestAttendee() booking.Attendee {
	return booking.Attendee{Name: "Bill Mathers", Email: "bill@example.com", IANATimezone: guestZone}
}

// With no model, what a guest types after "Suggest another time" cannot be read as times. It
// is kept for the owner in their own words, and the chat still has a way to end.
func TestPersonalLinkChat_withoutAModelASuggestionIsKeptAsANote(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	f.h.SetLLM(nil)
	f.chat(t, f.token, plinkChatRequest{Action: "suggest"})
	res := f.say(t, "Friday at 3pm would be better")
	if got := kinds(res.Items); len(got) != 2 || !res.Items[1].Done {
		t.Fatalf("reply = %v; want an acknowledgement and the closing question", got)
	}
	if held := f.held(t); len(held) != 1 || held[0].Content != "Friday at 3pm would be better" {
		t.Fatalf("held = %+v", held)
	}
}
