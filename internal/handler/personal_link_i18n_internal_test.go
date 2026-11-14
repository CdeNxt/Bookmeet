package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/i18n"
	"github.com/calnode/calnode/internal/personallink"
)

// chatIn is chat with the guest's browser asking for a language.
func (f *chatFixture) chatIn(t *testing.T, lang string, req plinkChatRequest) plinkChatResponse {
	t.Helper()
	if req.Timezone == "" {
		req.Timezone = guestZone
	}
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/links/"+f.token+"/chat", strings.NewReader(string(body)))
	r.SetPathValue("token", f.token)
	r.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	f.h.PersonalLinkChat(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat %q: %d - %s", req.Action, rec.Code, rec.Body.String())
	}
	var out plinkChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A guest whose browser asks for French reads French all the way through, with a 24-hour
// clock and French day names. The owner's Telegram message stays English, and the booking
// remembers the guest's language so its reminders match.
func TestPersonalLinkChat_theGuestReadsTheirOwnLanguageAndTheOwnerReadsEnglish(t *testing.T) {
	f := newChatFixture(t)
	fr := i18n.Get("fr")

	res := f.chatIn(t, "fr-FR,fr;q=0.9", plinkChatRequest{Action: "open"})
	greeting := res.Items[0].Text
	for _, want := range []string{"Bonjour Bill, Jeroen m’a demandé d’organiser une réunion de 30 minutes avec vous", "C’est au sujet de the contract.", "adresse e-mail"} {
		if !strings.Contains(greeting, want) {
			t.Errorf("greeting is missing %q:\n%s", want, greeting)
		}
	}
	if l := f.reload(t); l.GuestLocale != "fr" {
		t.Errorf("link's guest language = %q; want fr", l.GuestLocale)
	}

	f.chatIn(t, "fr", plinkChatRequest{Action: "say", Text: "bill@example.com"})
	res = f.chatIn(t, "fr", plinkChatRequest{Action: "skip_phone"})
	times := res.Items[len(res.Items)-1]
	if times.Kind != "times" || len(times.Days) == 0 {
		t.Fatalf("after the details: %v; want the times", kinds(res.Items))
	}
	if want := fr.FormatDay(f.at(17, 0).In(mustZone(t, guestZone))); times.Days[0].Label != want {
		t.Errorf("day label = %q; want %q", times.Days[0].Label, want)
	}
	if got := times.Days[0].Slots[0].Label; got != "09:00" {
		t.Errorf("first time = %q; want a 24-hour clock", got)
	}
	if !strings.Contains(times.Head, "heure de Los Angeles") {
		t.Errorf("times heading = %q; want the zone named in French", times.Head)
	}

	start := f.at(17, 30)
	f.chatIn(t, "fr", plinkChatRequest{Action: "pick", Start: start.Format(time.RFC3339)})
	sent := len(f.tg.sent(ownerChat))
	res = f.chatIn(t, "fr", plinkChatRequest{Action: "confirm", Start: start.Format(time.RFC3339)})
	if res.Stage != "booked" || res.Items[0].Title != "C’est réservé." {
		t.Fatalf("after confirming: stage %q, title %q", res.Stage, res.Items[0].Title)
	}
	if row := res.Items[0].Rows[1]; row[0] != "Durée" || row[1] != "30 min" {
		t.Errorf("length row = %v", row)
	}

	told := f.tg.sent(ownerChat)
	if len(told) != sent+1 {
		t.Fatalf("the owner got %d messages; want 1", len(told)-sent)
	}
	for _, want := range []string{"Booked: Bill Mathers", "30 minutes", "5:30 PM to 6:00 PM UTC", "Where: Phone Call"} {
		if !strings.Contains(told[len(told)-1], want) {
			t.Errorf("the owner's message is missing %q:\n%s", want, told[len(told)-1])
		}
	}
	var locale string
	if err := f.h.db.QueryRow(`SELECT locale FROM booking_attendees WHERE is_organizer = 1`).Scan(&locale); err != nil || locale != "fr" {
		t.Errorf("booking's guest language = %q (%v); want fr", locale, err)
	}
}

// The page itself is drawn in the guest's language, and is handed only the handful of
// labels it draws.
func TestPersonalLinkPage_isDrawnInTheGuestsLanguage(t *testing.T) {
	f := newChatFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/i/"+f.token, nil)
	r.SetPathValue("token", f.token)
	r.Header.Set("Accept-Language", "de")
	rec := httptest.NewRecorder()
	f.h.PersonalLinkPage(rec, r)
	page := rec.Body.String()
	for _, want := range []string{`<html lang="de">`, "Termin mit Jeroen", "Andere Zeit vorschlagen", "Weitere Zeiten"} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(page, "Hallo %[1]s") {
		t.Error("the page was handed wording it does not draw")
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// expire makes the fixture's link run out: its expiry moved into the past, as time would.
func (f *chatFixture) expire(t *testing.T, ago time.Duration) {
	t.Helper()
	if _, err := f.h.db.Exec(`UPDATE personal_links SET expires_at = ? WHERE id = ?`,
		time.Now().Add(-ago).UTC().Format(time.RFC3339), f.link.ID); err != nil {
		t.Fatal(err)
	}
}

// A link that runs out unused is announced to the owner once, saying how far the guest got,
// with a button that starts a new one. Nothing is created by the tap itself.
func TestPersonalLinkSweep_aLinkThatRanOutIsToldOnceWithAWayToMakeANewOne(t *testing.T) {
	f := newChatFixture(t)
	f.chat(t, f.token, plinkChatRequest{Action: "open"})
	f.say(t, "bill@example.com")
	f.expire(t, time.Hour)

	sent := f.ownerMessages()
	f.h.sweepRanOut(context.Background())
	told := f.tg.sent(ownerChat)
	if len(told) != sent+1 {
		t.Fatalf("the owner got %d messages; want 1", len(told)-sent)
	}
	for _, want := range []string{"Bill Mathers's link ran out without a booking", "Bill gave an email (bill@example.com) and stopped there."} {
		if !strings.Contains(told[len(told)-1], want) {
			t.Errorf("the message is missing %q:\n%s", want, told[len(told)-1])
		}
	}
	if l := f.reload(t); l.Status != "expired" || l.ExpiryToldAt.IsZero() {
		t.Errorf("link is %q, told at %v; want expired and told", l.Status, l.ExpiryToldAt)
	}

	// A second sweep says nothing more.
	f.h.sweepRanOut(context.Background())
	if n := f.ownerMessages(); n != sent+1 {
		t.Errorf("a second sweep sent %d more messages", n-sent-1)
	}

	// The button asks for new times; it makes no link by itself.
	data, ok := f.buttons()["New link for Bill"]
	if !ok {
		t.Fatalf("no New link button; have %v", f.buttons())
	}
	f.tapFrom(t, ownerChat, data)
	// (The fixture's times are still ahead, so the old offer comes back in the panel; with
	// the times gone the owner is asked for new ones instead.)
	if !f.anyText("Here is the old offer.") || !strings.Contains(f.panel(), "Bill Mathers") {
		t.Errorf("after the tap the old offer was not reopened: %v", f.texts())
	}
	if n := len(f.links(t)); n != 1 {
		t.Errorf("%d links after the tap; want still 1", n)
	}
}

// Old news is not announced: a link that ran out days ago is marked and left alone, and so
// is one whose guest booked.
func TestPersonalLinkSweep_oldOrUsedLinksAreNotAnnounced(t *testing.T) {
	f := newChatFixture(t)
	f.expire(t, 5*24*time.Hour)
	sent := f.ownerMessages()
	f.h.sweepRanOut(context.Background())
	if n := f.ownerMessages(); n != sent {
		t.Errorf("a link that ran out five days ago sent %d messages", n-sent)
	}
	if l := f.reload(t); l.ExpiryToldAt.IsZero() {
		t.Error("the old link was not marked, so it would be looked at every minute")
	}
}

// One link holds a limited number of things for the owner.
func TestPersonalLinkChat_aLinkHoldsOnlySoMuchForTheOwner(t *testing.T) {
	f := newChatFixture(t)
	links := f.h.personalLinks()
	for i := 0; i < personallink.MaxHeld; i++ {
		if err := links.Hold(context.Background(), f.link.ID, personallink.HeldNote, "note"); err != nil {
			t.Fatalf("hold %d: %v", i, err)
		}
	}
	if err := links.Hold(context.Background(), f.link.ID, personallink.HeldNote, "one more"); err != personallink.ErrTooManyHeld {
		t.Errorf("hold past the limit = %v; want ErrTooManyHeld", err)
	}
}

// Finished or abandoned details panels are cleared out; a recent one stays.
func TestTidyDrafts_removesOnlyOldPanels(t *testing.T) {
	f := newChatFixture(t)
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	for id, at := range map[string]string{"old": old, "new": now} {
		if _, err := f.h.db.Exec(`INSERT INTO assistant_drafts (id, user_id, chat_id, message_id, data, status, created_at, updated_at)
			VALUES (?, ?, 1, 1, '{}', 'done', ?, ?)`, id, f.userID, at, at); err != nil {
			t.Fatal(err)
		}
	}
	f.h.tidyDrafts(context.Background())
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts WHERE id = 'old'`); n != 0 {
		t.Error("the old panel was kept")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts WHERE id = 'new'`); n != 1 {
		t.Error("the recent panel was removed")
	}
}
