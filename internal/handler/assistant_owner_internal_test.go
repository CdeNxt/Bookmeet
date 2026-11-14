package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/llm"
	"github.com/calnode/calnode/internal/personallink"
)

// fakeModel stands in for the AI model. Each call answers with the next scripted message,
// so a test decides exactly what the "model" understood and asserts what the program did.
type fakeModel struct {
	mu       sync.Mutex
	srv      *httptest.Server
	script   []llm.Message
	requests [][]llm.Message // the messages each call was given
}

func newFakeModel(t *testing.T) *fakeModel {
	t.Helper()
	fm := &fakeModel{}
	fm.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		fm.mu.Lock()
		fm.requests = append(fm.requests, body.Messages)
		next := llm.Message{Role: "assistant", Content: "OK."}
		if len(fm.script) > 0 {
			next, fm.script = fm.script[0], fm.script[1:]
		}
		fm.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": next}}})
	}))
	t.Cleanup(fm.srv.Close)
	return fm
}

// says queues a plain reply.
func (fm *fakeModel) says(text string) {
	fm.mu.Lock()
	fm.script = append(fm.script, llm.Message{Role: "assistant", Content: text})
	fm.mu.Unlock()
}

// calls queues a tool call followed by a closing line, the usual shape of one turn.
func (fm *fakeModel) calls(tool string, args any) {
	raw, _ := json.Marshal(args)
	fm.mu.Lock()
	fm.script = append(fm.script,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: tool, Arguments: string(raw)}}}},
		llm.Message{Role: "assistant", Content: "Check the panel and tap the button."})
	fm.mu.Unlock()
}

// tool queues a tool call on its own, for turns where the program's answer ends the turn.
func (fm *fakeModel) tool(name string, args any) {
	raw, _ := json.Marshal(args)
	fm.mu.Lock()
	fm.script = append(fm.script, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: name, Arguments: string(raw)}}}})
	fm.mu.Unlock()
}

// toolResults returns every tool result the program handed back to the model.
func (fm *fakeModel) toolResults() []string {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var out []string
	if len(fm.requests) == 0 {
		return nil
	}
	for _, m := range fm.requests[len(fm.requests)-1] {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

const ownerChat = 111

type ownerFixture struct {
	*assistantFixture
	model  *fakeModel
	userID string
	etID   string
}

// newOwnerFixture is a workspace with one 30-minute event type, the assistant switched on
// and the owner's chat linked: everything a real owner has before their first message.
func newOwnerFixture(t *testing.T) *ownerFixture {
	t.Helper()
	f := &ownerFixture{assistantFixture: newAssistantFixture(t), model: newFakeModel(t)}
	if rec := f.createEventType(t, "intro", "calendar"); rec.Code != http.StatusCreated {
		t.Fatalf("create event type: %d - %s", rec.Code, rec.Body.String())
	}
	if err := f.h.db.QueryRow(`SELECT id, user_id FROM event_types WHERE slug = 'intro'`).Scan(&f.etID, &f.userID); err != nil {
		t.Fatal(err)
	}
	f.connectBot(t)
	f.h.SetLLM(llm.New(llm.Config{Endpoint: f.model.srv.URL, Model: "test"}))
	if rec := f.patch(t, `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("switch on: %d - %s", rec.Code, rec.Body.String())
	}
	f.link(t, ownerChat)
	return f
}

// date returns the YYYY-MM-DD of `days` days from now, in UTC (the fixture owner's zone).
func date(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02") }

func (f *ownerFixture) message(t *testing.T, text string) {
	t.Helper()
	if rec := f.deliver(t, f.secret(t), ownerChat, "private", text); rec.Code != http.StatusOK {
		t.Fatalf("deliver message: %d", rec.Code)
	}
}

// tapFrom delivers a button tap from the given chat.
func (f *ownerFixture) tapFrom(t *testing.T, chatID int64, data string) {
	t.Helper()
	body := fmt.Sprintf(`{"update_id":2,"callback_query":{"id":"cb","from":{"id":%d},"message":{"message_id":1,"chat":{"id":%d,"type":"private"}},"data":%q}}`,
		chatID, chatID, data)
	req := httptest.NewRequest(http.MethodPost, telegramWebhookPath, strings.NewReader(body))
	req.Header.Set(telegramSecretHeader, f.secret(t))
	rec := httptest.NewRecorder()
	f.h.TelegramWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deliver tap: %d", rec.Code)
	}
}

// buttons returns label -> callback data for the panel as it currently stands: the buttons
// of the most recent message sent or edited with a keyboard.
func (f *ownerFixture) buttons() map[string]string {
	f.tg.mu.Lock()
	defer f.tg.mu.Unlock()
	for i := len(f.tg.calls) - 1; i >= 0; i-- {
		c := f.tg.calls[i]
		if c.method != "sendMessage" && c.method != "editMessageText" {
			continue
		}
		markup, _ := c.body["reply_markup"].(map[string]any)
		rows, _ := markup["inline_keyboard"].([]any)
		if len(rows) == 0 {
			continue
		}
		out := map[string]string{}
		for _, row := range rows {
			for _, b := range row.([]any) {
				btn := b.(map[string]any)
				out[strings.TrimPrefix(btn["text"].(string), "✓ ")], _ = btn["callback_data"].(string)
			}
		}
		return out
	}
	return nil
}

// tap presses the panel button with this label.
func (f *ownerFixture) tap(t *testing.T, label string) {
	t.Helper()
	data, ok := f.buttons()[label]
	if !ok {
		t.Fatalf("no button %q on the panel; it has %v", label, f.buttons())
	}
	f.tapFrom(t, ownerChat, data)
}

// lastText is the text of the most recent message sent or edited in the owner's chat.
func (f *ownerFixture) texts() []string {
	f.tg.mu.Lock()
	defer f.tg.mu.Unlock()
	var out []string
	for _, c := range f.tg.calls {
		if (c.method == "sendMessage" || c.method == "editMessageText") && c.body["chat_id"] == float64(ownerChat) {
			out = append(out, c.body["text"].(string))
		}
	}
	return out
}

// panel is the text of the live panel: the most recent message that has buttons under it.
func (f *ownerFixture) panel() string {
	f.tg.mu.Lock()
	defer f.tg.mu.Unlock()
	for i := len(f.tg.calls) - 1; i >= 0; i-- {
		c := f.tg.calls[i]
		if c.method != "sendMessage" && c.method != "editMessageText" {
			continue
		}
		markup, _ := c.body["reply_markup"].(map[string]any)
		if rows, _ := markup["inline_keyboard"].([]any); len(rows) > 0 {
			return c.body["text"].(string)
		}
	}
	return ""
}

func (f *ownerFixture) lastText() string {
	all := f.texts()
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

func (f *ownerFixture) anyText(substr string) bool {
	for _, s := range f.texts() {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func (f *ownerFixture) links(t *testing.T) []*personallink.Link {
	t.Helper()
	ls, err := f.h.personalLinks().List(context.Background(), f.userID)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func (f *ownerFixture) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := f.h.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func billWindows() map[string]any {
	return map[string]any{
		"guest_name": "Bill Mathers", "event_type": "intro", "topic": "the contract",
		"windows": []map[string]string{
			{"date": date(3), "start": "09:00", "end": "12:00"},
			{"date": date(5), "start": "13:00", "end": "16:00"},
		},
	}
}

// A thin request gets a question back, and the model is given what it needs to ask a good
// one: the owner's meeting types and a calendar to read dates from.
func TestOwnerAssistant_aThinRequestGetsAFollowUpQuestion(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.says("Sure. Which days and times should I offer Bill?")
	f.message(t, "set something up with Bill")

	if got := f.lastText(); got != "Sure. Which days and times should I offer Bill?" {
		t.Fatalf("reply = %q", got)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts`); n != 0 {
		t.Errorf("%d panels exist after a question", n)
	}
	system := f.model.requests[0][0].Content
	for _, want := range []string{`slug "intro"`, date(0) + " (today)", date(7), "Jeroen Host"} {
		if !strings.Contains(system, want) {
			t.Errorf("the model's instructions are missing %q", want)
		}
	}
}

// The model's only way to act is to show the panel. Nothing exists until the owner taps.
func TestOwnerAssistant_thePanelComesFirstAndTheTapCreatesTheLink(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", billWindows())
	f.message(t, "30 min with Bill Mathers about the contract, in 3 days 9 to 12 or in 5 days 1 to 4")

	var panel string
	for _, s := range f.texts() {
		if strings.Contains(s, "Check the details") {
			panel = s
		}
	}
	for _, want := range []string{"Guest: Bill Mathers", "Topic: the contract", "9:00 AM to 12:00 PM", "1:00 PM to 4:00 PM", "single use"} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel is missing %q:\n%s", want, panel)
		}
	}
	if _, ok := f.buttons()["Create link"]; !ok {
		t.Fatalf("panel buttons = %v; want a Create link button", f.buttons())
	}
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("%d links exist before the owner tapped", n)
	}

	f.tap(t, "Create link")
	links := f.links(t)
	if len(links) != 1 {
		t.Fatalf("%d links after the tap; want 1", len(links))
	}
	l := links[0]
	if l.GuestName != "Bill Mathers" || l.Topic != "the contract" || l.EventTypeID != f.etID {
		t.Errorf("link = %+v", l)
	}
	// Each day keeps its own hours, converted from the owner's zone to instants.
	day3, _ := time.Parse("2006-01-02", date(3))
	day5, _ := time.Parse("2006-01-02", date(5))
	want := []personallink.Window{
		{Start: day3.Add(9 * time.Hour), End: day3.Add(12 * time.Hour)},
		{Start: day5.Add(13 * time.Hour), End: day5.Add(16 * time.Hour)},
	}
	if len(l.Windows) != 2 || !l.Windows[0].Start.Equal(want[0].Start) || !l.Windows[0].End.Equal(want[0].End) ||
		!l.Windows[1].Start.Equal(want[1].Start) || !l.Windows[1].End.Equal(want[1].End) {
		t.Errorf("windows = %v; want %v", l.Windows, want)
	}

	// The owner is handed a link that really opens this invitation, and a message to paste.
	token, _ := f.h.personalLinks().Token(context.Background(), f.userID, l.ID)
	url := "https://book.team.example/i/" + token
	if got := f.lastText(); !strings.Contains(got, url) || !strings.Contains(got, "Ready to paste") {
		t.Errorf("the link message is missing the link or the paste text:\n%s", got)
	}
	if !f.anyText("Link created.") {
		t.Error("the panel was not closed out as created")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts WHERE status = 'open'`); n != 0 {
		t.Errorf("%d panels still open after creating", n)
	}
}

// What the model hands over is checked before it reaches the owner, and a refusal goes back
// to the model as something to ask, not to the owner as a broken panel.
func TestOwnerAssistant_whatTheModelGotWrongIsRefusedBeforeThePanel(t *testing.T) {
	cases := map[string]map[string]any{
		"an event type the owner does not have": {"guest_name": "Bill", "event_type": "strategy-day",
			"windows": []map[string]string{{"date": date(3), "start": "09:00", "end": "12:00"}}},
		"a date in the past": {"guest_name": "Bill", "event_type": "intro",
			"windows": []map[string]string{{"date": date(-2), "start": "09:00", "end": "12:00"}}},
		"a window shorter than the meeting": {"guest_name": "Bill", "event_type": "intro",
			"windows": []map[string]string{{"date": date(3), "start": "09:00", "end": "09:15"}}},
		"no times at all": {"guest_name": "Bill", "event_type": "intro"},
		"no guest":        {"event_type": "intro", "windows": []map[string]string{{"date": date(3), "start": "09:00", "end": "12:00"}}},
		"a place on a meeting type with no in-person option": {"guest_name": "Bill", "event_type": "intro", "place_query": "Equator Coffee",
			"windows": []map[string]string{{"date": date(3), "start": "09:00", "end": "12:00"}}},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t)
			f.model.calls("show_details", args)
			f.message(t, "set it up")
			results := f.model.toolResults()
			if len(results) != 1 || !strings.HasPrefix(results[0], "error:") {
				t.Fatalf("tool result = %v; want one error for the model to act on", results)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts`); n != 0 {
				t.Errorf("a panel was shown anyway")
			}
		})
	}
}

func TestOwnerAssistant_thePanelsButtonsAdjustItInPlace(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", billWindows())
	f.message(t, "set it up")
	sends := len(f.tg.of("sendMessage"))

	f.tap(t, "Times")
	f.tap(t, "Afternoon, 1 to 5")
	if got := f.lastText(); strings.Contains(got, "9:00 AM") || strings.Count(got, "1:00 PM to 5:00 PM") != 2 {
		t.Fatalf("after choosing Afternoon for every day:\n%s", got)
	}

	f.tap(t, "Days")
	added := time.Now().UTC().AddDate(0, 0, 4).Format("Mon 2 Jan")
	f.tap(t, added)
	f.tap(t, "Done")
	if got := f.lastText(); strings.Count(got, "1:00 PM to 5:00 PM") != 3 || !strings.Contains(got, added) {
		t.Fatalf("after adding a day:\n%s", got)
	}
	// The same message was rewritten each time; the chat did not fill with panels.
	if got := len(f.tg.of("sendMessage")); got != sends {
		t.Errorf("%d new messages were sent while adjusting; want the panel edited in place", got-sends)
	}

	f.tap(t, "Create link")
	if l := f.links(t); len(l) != 1 || len(l[0].Windows) != 3 {
		t.Fatalf("link windows = %v; want the three days on the panel", l)
	}
}

func TestOwnerAssistant_oneExactTimeWithAnEmailIsBookedOnTheTap(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", map[string]any{
		"guest_name": "Bill Mathers", "event_type": "intro", "guest_email": "Bill@Example.com",
		"book_at": map[string]string{"date": date(3), "time": "10:00"},
	})
	f.message(t, "book Bill Mathers in 3 days at 10, bill@example.com")

	if !f.anyText("Book this meeting?") {
		t.Fatalf("no booking panel; last message:\n%s", f.lastText())
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Fatalf("%d bookings exist before the owner tapped", n)
	}

	f.tap(t, "Book it")
	var start, email string
	if err := f.h.db.QueryRow(`SELECT b.start_at, a.email FROM bookings b JOIN booking_attendees a ON a.booking_id = b.id`).Scan(&start, &email); err != nil {
		t.Fatalf("read booking: %v", err)
	}
	day, _ := time.Parse("2006-01-02", date(3))
	if got, _ := time.Parse(time.RFC3339, start); !got.Equal(day.Add(10 * time.Hour)) {
		t.Errorf("booked at %s; want %s", start, day.Add(10*time.Hour))
	}
	if !strings.EqualFold(email, "bill@example.com") {
		t.Errorf("attendee email %q", email)
	}
	// Booked directly: no personal link, and the owner gets the manage link to pass on.
	if n := len(f.links(t)); n != 0 {
		t.Errorf("%d personal links were made for a direct booking", n)
	}
	got := f.lastText()
	if !strings.Contains(got, "Booked: Bill Mathers") || !strings.Contains(got, "https://book.team.example/manage/") {
		t.Errorf("booked message:\n%s", got)
	}
}

// Without an email the meeting cannot be booked for the guest, so one exact time becomes a
// link that offers only that time.
func TestOwnerAssistant_oneExactTimeWithoutAnEmailBecomesASingleTimeLink(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", map[string]any{
		"guest_name": "Bill Mathers", "event_type": "intro",
		"book_at": map[string]string{"date": date(3), "time": "10:00"},
	})
	f.message(t, "Bill Mathers in 3 days at 10")
	f.tap(t, "Create link")

	links := f.links(t)
	if len(links) != 1 || len(links[0].Windows) != 1 {
		t.Fatalf("links = %v; want one link with one window", links)
	}
	day, _ := time.Parse("2006-01-02", date(3))
	w := links[0].Windows[0]
	if !w.Start.Equal(day.Add(10*time.Hour)) || !w.End.Equal(day.Add(10*time.Hour+30*time.Minute)) {
		t.Errorf("window %v; want exactly the 30 minutes from 10:00", w)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 0 {
		t.Errorf("a booking was made without the guest's email")
	}
}

func TestOwnerAssistant_aDirectBookingOnABusyTimeBooksNothing(t *testing.T) {
	f := newOwnerFixture(t)
	day, _ := time.Parse("2006-01-02", date(3))
	lf := &linkFixture{inviteFixture: f.inviteFixture}
	if _, err := f.h.createBookingForSlug(context.Background(), "intro", day.Add(10*time.Hour), lf.guest(), nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	f.model.calls("show_details", map[string]any{
		"guest_name": "Bill Mathers", "event_type": "intro", "guest_email": "bill@example.com",
		"book_at": map[string]string{"date": date(3), "time": "10:00"},
	})
	f.message(t, "book Bill at 10")
	f.tap(t, "Book it")

	if n := f.count(t, `SELECT COUNT(*) FROM bookings`); n != 1 {
		t.Fatalf("%d bookings; want only the one that was already there", n)
	}
	if !f.anyText("isn't free on your calendar") {
		t.Errorf("the owner was not told the time is taken; last message:\n%s", f.lastText())
	}
	if n := f.count(t, `SELECT COUNT(*) FROM assistant_drafts WHERE status = 'open'`); n != 1 {
		t.Errorf("the panel was closed after a booking that did not happen")
	}
}

// An in-person meeting needs an address the owner confirmed. The model only names the place;
// the lookup and the choice are the program's and the owner's.
func TestOwnerAssistant_aPlaceIsLookedUpAndTheOwnerPicksIt(t *testing.T) {
	f := newOwnerFixture(t)
	if _, err := f.h.db.Exec(`UPDATE event_types SET allow_in_person = 1 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKMEET_GOOGLE_PLACES_KEY", "test-key")
	var asked string
	old := searchPlaces
	searchPlaces = func(_ context.Context, _, query string) ([]string, error) {
		asked = query
		return []string{"Equator Coffee, 100 Example Street, Mill Valley", "Equator Coffee, 220 Sample Avenue, Sausalito"}, nil
	}
	t.Cleanup(func() { searchPlaces = old })

	args := billWindows()
	args["place_query"] = "Equator Coffee"
	f.model.calls("show_details", args)
	f.message(t, "coffee with Bill at Equator Coffee")

	if asked != "Equator Coffee" {
		t.Fatalf("looked up %q; want the place as the owner named it", asked)
	}
	if got := f.panel(); !strings.Contains(got, "Which Equator Coffee?") || !strings.Contains(got, "not confirmed yet") {
		t.Fatalf("panel does not ask which place:\n%s", got)
	}
	if _, ok := f.buttons()["Create link"]; ok {
		t.Fatal("Create link is offered before a place was picked")
	}

	f.tap(t, "Equator Coffee, 220 Sample Avenue, Sausalito")
	if got := f.panel(); !strings.Contains(got, "Where: Equator Coffee, 220 Sample Avenue, Sausalito") {
		t.Fatalf("panel after picking:\n%s", got)
	}
	f.tap(t, "Create link")
	l := f.links(t)
	if len(l) != 1 || l[0].PlaceName != "Equator Coffee" || l[0].PlaceAddress != "Equator Coffee, 220 Sample Avenue, Sausalito" {
		t.Fatalf("link place = %+v", l)
	}
}

func TestOwnerAssistant_existingLinksCanBeListedResentCancelledAndChanged(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", billWindows())
	f.message(t, "set it up")
	f.tap(t, "Create link")
	first := f.links(t)[0]
	token, _ := f.h.personalLinks().Token(context.Background(), f.userID, first.ID)

	f.model.calls("list_links", map[string]any{})
	f.message(t, "show my links")
	if !f.anyText("Bill Mathers · Intro") {
		t.Errorf("the list does not show Bill's link; last message:\n%s", f.lastText())
	}

	// The lost-link case: the same link comes back, not a new one.
	f.model.calls("resend_link", map[string]any{"guest_name": "bill"})
	f.message(t, "send me Bill's link again")
	shown := 0
	for _, s := range f.texts() {
		if strings.Contains(s, "/i/"+token) {
			shown++
		}
	}
	if shown != 2 {
		t.Errorf("the original link was shown %d times; want twice (when made, and when resent)", shown)
	}
	if n := len(f.links(t)); n != 1 {
		t.Errorf("resending made %d links", n)
	}

	// Changing the offer makes a new link and retires the old one.
	f.model.calls("change_link", map[string]any{"guest_name": "Bill Mathers"})
	f.message(t, "change Bill's offer")
	f.tap(t, "Create new link")
	links := f.links(t)
	if len(links) != 2 {
		t.Fatalf("%d links after changing; want the old and the new", len(links))
	}
	if links[0].ReplacesID != first.ID || links[1].Status != personallink.StatusReplaced {
		t.Errorf("new link replaces %q, old link is %q; want the old one replaced", links[0].ReplacesID, links[1].Status)
	}

	// Cancelling waits for a tap; the model asking is not enough.
	f.model.calls("cancel_link", map[string]any{"guest_name": "Bill"})
	f.message(t, "cancel Bill's link")
	if got := f.links(t)[0].Status; got != personallink.StatusOpen {
		t.Fatalf("link is %q before the owner confirmed", got)
	}
	f.tap(t, "Cancel link")
	if got := f.links(t)[0].Status; got != personallink.StatusCancelled {
		t.Errorf("link is %q after confirming; want cancelled", got)
	}
}

// A tap is only as good as the chat it came from, and a panel that was closed stays closed.
func TestOwnerAssistant_tapsFromElsewhereAndOnDeadPanelsDoNothing(t *testing.T) {
	f := newOwnerFixture(t)
	f.model.calls("show_details", billWindows())
	f.message(t, "set it up")
	create := f.buttons()["Create link"]

	f.tapFrom(t, 999, create) // someone who is not the owner replaying the button
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("a tap from an unlinked chat created %d links", n)
	}

	f.tap(t, "Discard")
	f.tapFrom(t, ownerChat, create) // the owner tapping a panel they discarded
	if n := len(f.links(t)); n != 0 {
		t.Fatalf("a discarded panel created %d links", n)
	}
	answers := f.tg.of("answerCallbackQuery")
	if last := answers[len(answers)-1]; last["text"] != "That panel is no longer active." {
		t.Errorf("tap on a dead panel answered %v", last)
	}
}

// A reasoning model that runs out of room mid-thought returns nothing usable. The owner must
// never be left looking at "typing…" with no answer: the assistant asks the model once more,
// and if that fails too, says so itself.
func TestOwnerAssistant_anEmptyReplyIsNeverSilence(t *testing.T) {
	f := newOwnerFixture(t)
	before := len(f.texts())

	f.model.says("<think>The owner wants a meeting with Bill, so first I should work out")
	f.model.says("Which days should I offer Bill?")
	f.message(t, "set something up with Bill")
	if got := f.lastText(); got != "Which days should I offer Bill?" {
		t.Fatalf("after one empty reply the owner got %q; want the retry's answer", got)
	}

	f.model.says("<think>still thinking")
	f.model.says("")
	f.message(t, "next week")
	if got := f.lastText(); !strings.Contains(got, "didn't manage to answer") {
		t.Fatalf("after two empty replies the owner got %q; want the program to say so", got)
	}
	if n := len(f.texts()) - before; n != 2 {
		t.Errorf("the owner got %d messages for two requests; want one each", n)
	}

	// A tool that already showed the owner something needs no closing line: an empty
	// reply after it is fine, and gets no apology.
	f.model.tool("list_links", map[string]any{})
	f.model.says("")
	count := len(f.texts())
	f.message(t, "show my links")
	if got := len(f.texts()) - count; got != 1 || strings.Contains(f.lastText(), "didn't manage") {
		t.Errorf("after a tool spoke the owner got %d messages, ending %q; want just the tool's", got, f.lastText())
	}
}
