package handler

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
	"github.com/calnode/calnode/internal/llm"
	"github.com/calnode/calnode/internal/mailer"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/slots"
)

// noTelegram is an owner with email and no text channel.
func noTelegram(f *ownerFixture) { f.h.SetTelegram(nil, "") }

// approvalMails returns the approval emails sent so far, and the last one's token.
func approvalMails(t *testing.T, f *ownerFixture) (int, string, string) {
	t.Helper()
	f.mail.mu.Lock()
	defer f.mail.mu.Unlock()
	re := regexp.MustCompile(`/approve/([A-Za-z0-9_-]+)`)
	n, token, text := 0, "", ""
	for _, m := range f.mail.msgs {
		if found := re.FindStringSubmatch(m.Text); found != nil {
			if len(m.To) != 1 || m.To[0] != inviteHostEmail {
				t.Fatalf("an approval went to %v", m.To)
			}
			n, token, text = n+1, found[1], m.Subject+"\n"+m.Text
		}
	}
	return n, token, text
}

func signIn(t *testing.T, f *ownerFixture, userID string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := f.h.createSession(context.Background(), rec, userID); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0]
}

func approvalRequest(f *ownerFixture, token string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	method, body := http.MethodGet, ""
	if form != nil {
		method, body = http.MethodPost, form.Encode()
	}
	req := httptest.NewRequest(method, "/approve/"+token, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", token)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	if form != nil {
		f.h.DecideApproval(rec, req)
	} else {
		f.h.ApprovalPage(rec, req)
	}
	return rec
}

// formOf is the page's hidden fields plus the button pressed.
func formOf(page, do string) url.Values {
	v := url.Values{"do": {do}}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`).FindAllStringSubmatch(page, -1) {
		v.Set(m[1], html.UnescapeString(m[2]))
	}
	return v
}

// The whole route for a direct booking: the agent asks, the owner is emailed, opening the
// link changes nothing, only the signed-in owner can decide, what is new since the page was
// drawn is shown first, and the answer works once.
func TestEmailApproval_aDirectBookingNeedsTheSignedInOwnersTap(t *testing.T) {
	f := newOwnerFixture(t)
	noTelegram(f)
	f.closeDay(t, 3)
	cs := connectMCPInternal(t, f.h)
	args := map[string]any{"guest_name": "Ann Agent", "guest_email": "ann@example.com", "event_type": "intro", "date": date(3), "time": "09:00", "ask_owner": true}
	if msg := toolError(t, cs, "book_for_guest", args); !strings.Contains(msg, "emailed") {
		t.Fatalf("the agent was told: %s", msg)
	}
	n, token, text := approvalMails(t, f)
	if n != 1 || !strings.Contains(text, "outside the hours") {
		t.Fatalf("%d approval emails: %s", n, text)
	}
	var stored string
	if err := f.h.db.QueryRow(`SELECT token_hash FROM owner_email_approvals`).Scan(&stored); err != nil || stored == token {
		t.Fatalf("the token is stored as sent (%v)", err)
	}
	bookings := func() int { return f.count(t, `SELECT COUNT(*) FROM bookings`) }

	if rec := approvalRequest(f, token, nil, nil); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "Ann Agent") {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Body.String())
	}
	if rec := approvalRequest(f, token, nil, url.Values{"do": {"book"}}); rec.Code != http.StatusUnauthorized || bookings() != 0 {
		t.Fatalf("a signed-out press: %d", rec.Code)
	}
	if _, err := f.h.db.Exec(`INSERT INTO users (id, email, name, iana_timezone) VALUES ('other', 'other@team.example', 'Other', 'UTC')`); err != nil {
		t.Fatal(err)
	}
	other := signIn(t, f, "other")
	if rec := approvalRequest(f, token, other, nil); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Ann Agent") {
		t.Fatalf("another user read it: %d %s", rec.Code, rec.Body.String())
	}
	if rec := approvalRequest(f, token, other, url.Values{"do": {"book"}}); rec.Code != http.StatusNotFound || bookings() != 0 {
		t.Fatalf("another user decided it: %d", rec.Code)
	}

	owner := signIn(t, f, f.userID)
	page := approvalRequest(f, token, owner, nil).Body.String()
	if !strings.Contains(page, "Book anyway") || bookings() != 0 {
		t.Fatalf("page (%d bookings):\n%s", bookings(), page)
	}
	day, _ := time.Parse("2006-01-02", date(3))
	at := day.Add(9 * time.Hour)
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: at, End: at.Add(30 * time.Minute)}}
	f.cal.titled = []calendar.BusyEvent{{Title: "Dentist", Start: at, End: at.Add(30 * time.Minute)}}
	f.cal.mu.Unlock()
	changed := approvalRequest(f, token, owner, formOf(page, "book")).Body.String()
	if !strings.Contains(changed, "Something changed") || !strings.Contains(changed, "Dentist") || bookings() != 0 {
		t.Fatalf("a new clash (%d bookings):\n%s", bookings(), changed)
	}
	if out := approvalRequest(f, token, owner, formOf(changed, "book")).Body.String(); !strings.Contains(out, "Booked Ann Agent") || bookings() != 1 {
		t.Fatalf("Book anyway (%d bookings):\n%s", bookings(), out)
	}
	if out := approvalRequest(f, token, owner, formOf(changed, "book")).Body.String(); !strings.Contains(out, "Already answered") || bookings() != 1 {
		t.Fatalf("a second press (%d bookings):\n%s", bookings(), out)
	}
}

// A link outside the usual hours, approved from the email; and an expired request.
func TestEmailApproval_aLinkAndExpiry(t *testing.T) {
	f := newOwnerFixture(t)
	noTelegram(f)
	f.closeDay(t, 3)
	cs := connectMCPInternal(t, f.h)
	args := offerArgs(3, "09:00", "11:00")
	args["ask_owner"] = true
	if msg := toolError(t, cs, "create_personal_link", args); !strings.Contains(msg, "emailed") {
		t.Fatalf("the agent was told: %s", msg)
	}
	_, token, _ := approvalMails(t, f)
	owner := signIn(t, f, f.userID)
	if _, err := f.h.db.Exec(`UPDATE owner_email_approvals SET expires_at = '2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if rec := approvalRequest(f, token, owner, url.Values{"do": {"create"}}); rec.Code != http.StatusGone || len(f.links(t)) != 0 {
		t.Fatalf("an expired request: %d", rec.Code)
	}
	toolError(t, cs, "create_personal_link", args)
	_, token, _ = approvalMails(t, f)
	out := approvalRequest(f, token, owner, url.Values{"do": {"create"}}).Body.String()
	links := f.links(t)
	if len(links) != 1 || !links[0].OutsideAvailability || !strings.Contains(out, "/i/") {
		t.Fatalf("links = %+v\n%s", links, out)
	}
}

// A guest suggests times and the owner has no text channel: one email, and the page books
// the time the owner picks over their calendar, naming it there and not in the email.
func TestEmailApproval_aGuestsSuggestedTimes(t *testing.T) {
	f := newChatFixture(t)
	f.throughDetails(t)
	noTelegram(f.ownerFixture)
	clash := f.at(21, 0)
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: clash, End: clash.Add(30 * time.Minute)}}
	f.cal.titled = []calendar.BusyEvent{{Title: "Dentist", Start: clash, End: clash.Add(30 * time.Minute)}}
	f.cal.mu.Unlock()
	links := f.h.personalLinks()
	starts, _ := json.Marshal([]string{clash.UTC().Format(time.RFC3339)})
	for kind, content := range map[string]string{personallink.HeldSuggestions: string(starts), personallink.HeldNote: "<b>after lunch</b>"} {
		if err := links.Hold(context.Background(), f.link.ID, kind, content); err != nil {
			t.Fatal(err)
		}
	}
	f.h.reportChat(context.Background(), f.link.ID)
	f.h.reportChat(context.Background(), f.link.ID)
	n, token, text := approvalMails(t, f.ownerFixture)
	if n != 1 || strings.Contains(text, "Dentist") || !strings.Contains(text, "after lunch") {
		t.Fatalf("%d emails:\n%s", n, text)
	}
	owner := signIn(t, f.ownerFixture, f.userID)
	page := approvalRequest(f.ownerFixture, token, owner, nil).Body.String()
	if !strings.Contains(page, "Dentist") || strings.Contains(page, "<b>after lunch") {
		t.Fatalf("page:\n%s", page)
	}
	// A press that did not come from a page showing the clash does not book over it.
	approvalRequest(f.ownerFixture, token, owner, url.Values{"do": {"time-0"}})
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("%d bookings from a press that showed nothing", n)
	}
	out := approvalRequest(f.ownerFixture, token, owner, formOf(page, "time-0")).Body.String()
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings after Book anyway:\n%s", n, out)
	}
}

// With no way to ask, the agent is told so; a card app is never emailed.
func TestEmailApproval_onlyWhenThereIsNoOtherWay(t *testing.T) {
	f := newOwnerFixture(t)
	f.closeDay(t, 3)
	args := map[string]any{"guest_name": "Ann Agent", "guest_email": "ann@example.com", "event_type": "intro", "date": date(3), "time": "09:00", "ask_owner": true}
	// Telegram connected: it is asked there.
	toolError(t, connectMCPInternal(t, f.h), "book_for_guest", args)
	noTelegram(f)
	callRaw(t, connectCardApp(t, f.h), "book_for_guest", args)
	if n, _, _ := approvalMails(t, f); n != 0 {
		t.Fatalf("%d approval emails with a card or Telegram available", n)
	}
	f.h.SetMailer(&mailer.Noop{}, "https://book.team.example")
	if msg := toolError(t, connectMCPInternal(t, f.h), "book_for_guest", args); !strings.Contains(msg, "no way to ask") {
		t.Errorf("with no channel at all: %s", msg)
	}
}

// Email alone is enough to switch the assistant on: no text channel is required.
func TestEmailApproval_theAssistantSwitchesOnWithoutTelegram(t *testing.T) {
	f := newAssistantFixture(t)
	f.h.SetLLM(llm.New(llm.Config{Endpoint: "http://127.0.0.1:1/v1", Model: "test"}))
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("switch on with email only: %d - %s", rec.Code, rec.Body.String())
	}
	if s := f.settings(t); s["active"] != true || s["email_ready"] != true {
		t.Errorf("settings = %v", s)
	}
}
