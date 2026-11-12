package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/llm"
	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/telegram"
)

// The owner's side of the personal booking assistant: a Telegram conversation that ends in a
// personal booking link, or in a meeting booked directly.
//
// The division of labour is the same as the booking-page assistant's, and is the point of the
// design:
//
//   - The model turns the owner's words into a guest, a meeting type, days and times. It
//     never creates anything. Its one way to act is show_details, which puts a panel in front
//     of the owner.
//   - The program validates every value the model hands it, renders the panel, and does the
//     creating or booking - only when the owner taps a button. A tap is a button's callback
//     data, never text the model interprets.
//
// So a misheard date shows up on the panel before a link exists, and nothing the model says
// can book a meeting.

const (
	ownerMaxIters   = 5   // tool-loop iterations per owner message
	ownerMaxTokens  = 600 // output tokens per model call
	ownerHistory    = 16  // remembered messages fed back as context
	ownerRunTimeout = 90 * time.Second
	ownerMaxWindows = 14
	ownerDaysShown  = 10 // days offered in the panel's Days view
)

// ownerAssistantRules is the code-owned part of the owner assistant's prompt.
const ownerAssistantRules = `You are the Bookmeet booking assistant, talking with %s, the account owner, in Telegram. Behave like a sharp, discreet personal assistant.

What you do: set up a meeting with one guest. Either make a personal booking link limited to the times the owner chooses, or, when the owner names one exact time and gives the guest's email, book it directly. You never create or book anything yourself. You call show_details, which shows the owner a panel, and the owner taps a button to confirm.

You need: the guest's full name; the meeting type (one of the owner's, listed below, by its slug); and the days and times to offer. Different days may have different times. For an in-person meeting you also need the place.

Do not guess anything the owner has not said or clearly implied. When something is missing or ambiguous, ask a short follow-up (at most two questions in one message). If the owner has exactly one meeting type, use it without asking.

Dates: read them from the calendar below. Do not work out a date any other way. Times are in the owner's time zone and written as 24-hour HH:MM in tool calls.

One exact time: pass book_at. With guest_email it is booked directly once the owner taps; without an email the program makes a single-time link for the owner to send. If the owner names one exact time and has not said which they want, ask once whether they have the guest's email or would like a link.

Place: pass what the owner said as place_query. The program looks it up and the owner picks the right location by tapping. Never write, guess or confirm an address yourself, and do not ask the owner for an address.

Topic: if the owner says what the meeting is about, pass a short topic.

Changes: call show_details again with the full updated details. The owner can also adjust the panel with its buttons; the state below shows what it holds.

Existing links: list_links, resend_link, cancel_link and change_link work on links the owner made earlier, found by the guest's name.

You cannot message the guest, and nothing the owner says is passed on to the guest. If asked to tell the guest something, say so and offer what you can do: change the offer and give the owner a new link to send, or leave it with them.

You know nothing about what is in the owner's calendar. Never invent availability, people or past events.

Be brief, the way a person texting would be: one or two short sentences. Do not repeat what the panel shows. Plain text only: no markdown, no lists, no emoji.`

type ownerProfile struct {
	Name string
	Loc  *time.Location
	H24  bool
}

type ownerEventType struct {
	ID            string
	Slug          string
	Name          string
	Minutes       int
	LocationType  string
	AllowInPerson bool
}

// draftWindow is one offered stretch, in the owner's own zone: a date and minutes after
// midnight. It stays in local terms until a link is made so the panel shows exactly what the
// owner said, and is converted to instants once, at creation.
type draftWindow struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// ownerDraft is the details panel's contents.
type ownerDraft struct {
	ID        string `json:"-"`
	UserID    string `json:"-"`
	ChatID    int64  `json:"-"`
	MessageID int64  `json:"-"`

	GuestName   string        `json:"guest_name"`
	GuestEmail  string        `json:"guest_email,omitempty"`
	EventTypeID string        `json:"event_type_id"`
	Topic       string        `json:"topic,omitempty"`
	Windows     []draftWindow `json:"windows,omitempty"`

	// Direct is a booking of one exact time for a guest whose email the owner gave: no
	// link, no guest chat. BookDate/BookMinute hold that time.
	Direct     bool   `json:"direct,omitempty"`
	BookDate   string `json:"book_date,omitempty"`
	BookMinute int    `json:"book_minute,omitempty"`

	PlaceQuery   string   `json:"place_query,omitempty"`
	PlaceOptions []string `json:"place_options,omitempty"`
	Place        string   `json:"place,omitempty"` // confirmed by the owner's tap
	// PlaceUnverified marks a place taken as typed because this server has no place lookup.
	PlaceUnverified bool `json:"place_unverified,omitempty"`

	ReplacesLinkID string `json:"replaces_link_id,omitempty"`
	View           string `json:"view,omitempty"` // "", "type", "days", "times", "place"
}

var timePresets = []struct {
	Label      string
	Start, End int
}{
	{"Morning, 9 to 12", 9 * 60, 12 * 60},
	{"Afternoon, 1 to 5", 13 * 60, 17 * 60},
	{"All day, 9 to 5", 9 * 60, 17 * 60},
}

// searchPlaces is the place lookup; a variable so tests can stand in for Google.
var searchPlaces = searchGooglePlaces

// ownerLocks serialises one owner's messages and taps, so two arriving together cannot both
// act on the same panel.
var ownerLocks sync.Map

// assistantRun runs one piece of assistant work for an owner, off the webhook's request so
// Telegram is answered at once. Tests run it inline.
func (h *Handler) assistantRun(userID string, fn func(ctx context.Context)) {
	run := func() {
		mu, _ := ownerLocks.LoadOrStore(userID, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		defer mu.(*sync.Mutex).Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), ownerRunTimeout)
		defer cancel()
		fn(ctx)
	}
	if h.assistantInline {
		run()
		return
	}
	go run() // #nosec G118 -- deliberately detached from the webhook request, which returns immediately
}

// ownerSession is what one message or tap works with.
type ownerSession struct {
	h      *Handler
	bot    *telegram.Client
	userID string
	chatID int64
	prof   ownerProfile
	types  []ownerEventType
	now    time.Time
}

func (h *Handler) newOwnerSession(ctx context.Context, bot *telegram.Client, userID string, chatID int64) (*ownerSession, error) {
	s := &ownerSession{h: h, bot: bot, userID: userID, chatID: chatID, now: time.Now()}
	var tz, timeFormat string
	if err := h.db.QueryRowContext(ctx,
		`SELECT name, iana_timezone, time_format FROM users WHERE id = ?`, userID).Scan(&s.prof.Name, &tz, &timeFormat); err != nil {
		return nil, fmt.Errorf("owner profile: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	s.prof.Loc, s.prof.H24 = loc, timeFormat == "24h"

	rows, err := h.db.QueryContext(ctx, `
		SELECT id, slug, name, duration_minutes, location_type, allow_in_person
		FROM event_types WHERE user_id = ? AND is_active = 1 AND is_public = 1
		ORDER BY name LIMIT 12`, userID)
	if err != nil {
		return nil, fmt.Errorf("owner event types: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var et ownerEventType
		if err := rows.Scan(&et.ID, &et.Slug, &et.Name, &et.Minutes, &et.LocationType, &et.AllowInPerson); err != nil {
			return nil, err
		}
		s.types = append(s.types, et)
	}
	return s, rows.Err()
}

func (s *ownerSession) say(ctx context.Context, text string) {
	if _, err := s.bot.Send(ctx, s.chatID, text, nil); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: send", "error", err)
	}
}

func (s *ownerSession) typeByID(id string) *ownerEventType {
	for i := range s.types {
		if s.types[i].ID == id {
			return &s.types[i]
		}
	}
	return nil
}

func (s *ownerSession) typeBySlug(slug string) *ownerEventType {
	slug = strings.TrimSpace(slug)
	for i := range s.types {
		if strings.EqualFold(s.types[i].Slug, slug) || strings.EqualFold(s.types[i].Name, slug) {
			return &s.types[i]
		}
	}
	return nil
}

// ---- conversation memory ----

func (s *ownerSession) remember(ctx context.Context, role, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if len(content) > 2000 {
		content = content[:2000]
	}
	if _, err := s.h.db.ExecContext(ctx,
		`INSERT INTO assistant_messages (user_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		s.userID, role, content, time.Now().UTC().Format(time.RFC3339)); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: remember", "error", err)
		return
	}
	// Working memory, not a record: keep the last few dozen.
	_, _ = s.h.db.ExecContext(ctx, `
		DELETE FROM assistant_messages WHERE user_id = ? AND id NOT IN
		  (SELECT id FROM assistant_messages WHERE user_id = ? ORDER BY id DESC LIMIT 60)`, s.userID, s.userID)
}

func (s *ownerSession) history(ctx context.Context) []llm.Message {
	rows, err := s.h.db.QueryContext(ctx, `
		SELECT role, content FROM (
		  SELECT id, role, content FROM assistant_messages WHERE user_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id`, s.userID, ownerHistory)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []llm.Message
	for rows.Next() {
		var role, content string
		if rows.Scan(&role, &content) != nil {
			continue
		}
		if role == "event" {
			// Something the owner did by tapping, or the program did in response. The model
			// has no other way to know it happened.
			role, content = "user", "[event] "+content
		}
		out = append(out, llm.Message{Role: role, Content: content})
	}
	return out
}

// ---- drafts ----

func (s *ownerSession) openDraft(ctx context.Context) *ownerDraft {
	var d ownerDraft
	var data string
	err := s.h.db.QueryRowContext(ctx, `
		SELECT id, chat_id, message_id, data FROM assistant_drafts
		WHERE user_id = ? AND status = 'open' ORDER BY updated_at DESC, rowid DESC LIMIT 1`, s.userID).
		Scan(&d.ID, &d.ChatID, &d.MessageID, &data)
	if err != nil || json.Unmarshal([]byte(data), &d) != nil {
		return nil
	}
	d.UserID = s.userID
	return &d
}

func (s *ownerSession) draftByID(ctx context.Context, id string) *ownerDraft {
	var d ownerDraft
	var data string
	err := s.h.db.QueryRowContext(ctx, `
		SELECT id, chat_id, message_id, data FROM assistant_drafts
		WHERE id = ? AND user_id = ? AND status = 'open'`, id, s.userID).Scan(&d.ID, &d.ChatID, &d.MessageID, &data)
	if err != nil || json.Unmarshal([]byte(data), &d) != nil {
		return nil
	}
	d.UserID = s.userID
	return &d
}

func (s *ownerSession) saveDraft(ctx context.Context, d *ownerDraft) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if d.ID == "" {
		if d.ID, err = randomURLToken(8); err != nil {
			return err
		}
		d.UserID, d.ChatID = s.userID, s.chatID
		_, err = s.h.db.ExecContext(ctx, `
			INSERT INTO assistant_drafts (id, user_id, chat_id, message_id, data, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'open', ?, ?)`, d.ID, d.UserID, d.ChatID, d.MessageID, string(data), now, now)
		return err
	}
	_, err = s.h.db.ExecContext(ctx,
		`UPDATE assistant_drafts SET message_id = ?, data = ?, updated_at = ? WHERE id = ?`, d.MessageID, string(data), now, d.ID)
	return err
}

func (s *ownerSession) closeDraft(ctx context.Context, d *ownerDraft, status string) {
	_, _ = s.h.db.ExecContext(ctx, `UPDATE assistant_drafts SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().UTC().Format(time.RFC3339), d.ID)
}

// ---- time and wording helpers ----

func (s *ownerSession) clock(minute int) string {
	t := time.Date(2000, 1, 1, minute/60, minute%60, 0, 0, time.UTC)
	if s.prof.H24 {
		return t.Format("15:04")
	}
	return t.Format("3:04 PM")
}

func (s *ownerSession) day(date string) string {
	t, err := time.ParseInLocation("2006-01-02", date, s.prof.Loc)
	if err != nil {
		return date
	}
	return t.Format("Mon 2 Jan")
}

func (s *ownerSession) instant(date string, minute int) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", date, s.prof.Loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(t.Year(), t.Month(), t.Day(), minute/60, minute%60, 0, 0, s.prof.Loc), true
}

func (s *ownerSession) zone(date string) string {
	if t, ok := s.instant(date, 12*60); ok {
		return t.Format("MST")
	}
	return s.prof.Loc.String()
}

func parseClock(v string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func locationWord(locType string) string {
	switch locType {
	case "google_meet":
		return "Google Meet"
	case "zoom":
		return "Zoom"
	case "teams":
		return "Microsoft Teams"
	case "phone":
		return "phone call"
	case "in_person":
		return "in person"
	case "livekit":
		return "Bookmeet video"
	default:
		return "video call"
	}
}

func (et *ownerEventType) label() string {
	return fmt.Sprintf("%s (%d min, %s)", et.Name, et.Minutes, locationWord(et.LocationType))
}

func firstName(full string) string {
	if f := strings.Fields(full); len(f) > 0 {
		return f[0]
	}
	return full
}

func sortWindows(ws []draftWindow) {
	sort.Slice(ws, func(i, j int) bool {
		if ws[i].Date != ws[j].Date {
			return ws[i].Date < ws[j].Date
		}
		return ws[i].Start < ws[j].Start
	})
}

// ---- the message loop ----

// ownerHandleMessage answers one message from the owner.
func (h *Handler) ownerHandleMessage(ctx context.Context, bot *telegram.Client, userID string, chatID int64, text string) {
	s, err := h.newOwnerSession(ctx, bot, userID, chatID)
	if err != nil {
		h.logger.ErrorContext(ctx, "assistant: session", "error", err)
		return
	}
	client := h.getLLM()
	if client == nil {
		return
	}
	_ = bot.Typing(ctx, chatID)
	if len(s.types) == 0 {
		s.say(ctx, "You have no active, public event types yet. Create one in Bookmeet and I can set up meetings with it.")
		return
	}

	msgs := []llm.Message{{Role: "system", Content: s.systemPrompt(ctx)}}
	msgs = append(msgs, s.history(ctx)...)
	msgs = append(msgs, llm.Message{Role: "user", Content: text})
	s.remember(ctx, "user", text)

	for iter := 0; iter < ownerMaxIters; iter++ {
		res, err := client.Chat(ctx, llm.ChatRequest{Messages: msgs, Tools: ownerTools(), MaxTokens: ownerMaxTokens})
		if err != nil {
			h.logger.ErrorContext(ctx, "assistant: llm", "error", err)
			s.say(ctx, "I couldn't reach the AI model just now. Try again in a moment.")
			return
		}
		res.Message.Content = stripReasoning(res.Message.Content)
		msgs = append(msgs, res.Message)
		if len(res.Message.ToolCalls) == 0 {
			if reply := strings.TrimSpace(res.Message.Content); reply != "" {
				s.say(ctx, reply)
				s.remember(ctx, "assistant", reply)
			}
			return
		}
		for _, tc := range res.Message.ToolCalls {
			result := s.runTool(ctx, tc.Function.Name, tc.Function.Arguments)
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: result})
		}
	}
	s.say(ctx, "That took me too many steps. Tell me again in one message: who, what kind of meeting, and when.")
}

func (s *ownerSession) systemPrompt(ctx context.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, ownerAssistantRules, s.prof.Name)
	now := s.now.In(s.prof.Loc)
	fmt.Fprintf(&b, "\n\nNow: %s (%s).\nCalendar:\n", now.Format("Mon 2 Jan 2006, 15:04"), s.prof.Loc.String())
	for i := 0; i < 21; i++ {
		d := now.AddDate(0, 0, i)
		fmt.Fprintf(&b, "%s = %s", d.Format("Mon 2 Jan 2006"), d.Format("2006-01-02"))
		if i == 0 {
			b.WriteString(" (today)")
		}
		b.WriteString("\n")
	}
	b.WriteString("Meeting types:\n")
	for _, et := range s.types {
		fmt.Fprintf(&b, "- slug %q: %s", et.Slug, et.label())
		if et.AllowInPerson {
			b.WriteString(", can be in person at a place the owner names")
		}
		b.WriteString("\n")
	}
	if d := s.openDraft(ctx); d != nil {
		b.WriteString("Panel on screen now:\n" + s.draftSummary(d) + "\n")
	} else {
		b.WriteString("Panel on screen now: none.\n")
	}
	if links, err := s.h.personalLinks().List(ctx, s.userID); err == nil && len(links) > 0 {
		b.WriteString("Recent links:\n")
		for i, l := range links {
			if i == 6 {
				break
			}
			b.WriteString("- " + s.linkLine(l) + "\n")
		}
	}
	return b.String()
}

func ownerTools() []llm.Tool {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	byGuest := obj(map[string]any{"guest_name": str("The guest's name as the owner said it")}, "guest_name")
	return []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{
			Name:        "show_details",
			Description: "Show the owner the details panel for a meeting to confirm by tapping. Pass the full details every time. Returns what the panel now shows, or an error saying what to ask the owner.",
			Parameters: obj(map[string]any{
				"guest_name": str("The guest's full name"),
				"event_type": str("Slug of one of the owner's meeting types"),
				"windows": map[string]any{
					"type": "array", "description": "Times to offer, one entry per day and stretch. Omit when using book_at.",
					"items": obj(map[string]any{
						"date": str("YYYY-MM-DD from the calendar"), "start": str("HH:MM, 24-hour"), "end": str("HH:MM, 24-hour"),
					}, "date", "start", "end"),
				},
				"book_at": obj(map[string]any{
					"date": str("YYYY-MM-DD from the calendar"), "time": str("HH:MM, 24-hour start time"),
				}, "date", "time"),
				"guest_email": str("The guest's email, only if the owner gave it"),
				"topic":       str("What the meeting is about, a few words"),
				"place_query": str("For an in-person meeting: the place as the owner named it"),
			}, "guest_name", "event_type"),
		}},
		{Type: "function", Function: llm.ToolFunction{Name: "list_links", Description: "Show the owner their personal links with each one's status."}},
		{Type: "function", Function: llm.ToolFunction{Name: "resend_link", Description: "Send the owner a guest's existing link again.", Parameters: byGuest}},
		{Type: "function", Function: llm.ToolFunction{Name: "cancel_link", Description: "Ask the owner to confirm cancelling a guest's link.", Parameters: byGuest}},
		{Type: "function", Function: llm.ToolFunction{Name: "change_link", Description: "Open a guest's existing link in the details panel so the owner can change the offer and get a new link.", Parameters: byGuest}},
	}
}

func (s *ownerSession) runTool(ctx context.Context, name, argsJSON string) string {
	switch name {
	case "show_details":
		return s.toolShowDetails(ctx, argsJSON)
	case "list_links":
		return s.toolListLinks(ctx)
	case "resend_link", "cancel_link", "change_link":
		var args struct {
			GuestName string `json:"guest_name"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		l := s.findLink(ctx, args.GuestName)
		if l == nil {
			return "error: no link found for a guest by that name. Ask the owner which guest they mean, or call list_links."
		}
		switch name {
		case "resend_link":
			return s.toolResendLink(ctx, l)
		case "cancel_link":
			return s.toolCancelLink(ctx, l)
		default:
			return s.toolChangeLink(ctx, l)
		}
	}
	return "error: unknown tool"
}

// toolShowDetails validates what the model understood and puts the panel in front of the
// owner. Every refusal is worded for the model to act on: it says what to ask.
func (s *ownerSession) toolShowDetails(ctx context.Context, argsJSON string) string {
	var args struct {
		GuestName string `json:"guest_name"`
		EventType string `json:"event_type"`
		Windows   []struct {
			Date  string `json:"date"`
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"windows"`
		BookAt *struct {
			Date string `json:"date"`
			Time string `json:"time"`
		} `json:"book_at"`
		GuestEmail string `json:"guest_email"`
		Topic      string `json:"topic"`
		PlaceQuery string `json:"place_query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "error: the arguments were not valid JSON."
	}
	name := strings.TrimSpace(args.GuestName)
	if name == "" || len(name) > 200 {
		return "error: guest_name is missing. Ask the owner who the meeting is with."
	}
	et := s.typeBySlug(args.EventType)
	if et == nil {
		return "error: event_type is not one of the owner's meeting types. Ask which they want."
	}

	// Start from the panel on screen when it is for the same guest, so an adjustment keeps
	// what the owner already confirmed (a chosen place, a link being replaced).
	d := s.openDraft(ctx)
	if d == nil || !strings.EqualFold(d.GuestName, name) {
		if d != nil {
			s.retirePanel(ctx, d, "Replaced by a newer panel.")
		}
		d = &ownerDraft{}
	}
	d.GuestName, d.EventTypeID, d.View = name, et.ID, ""
	if t := strings.TrimSpace(args.Topic); t != "" {
		if len(t) > 200 {
			t = t[:200]
		}
		d.Topic = t
	}
	if e := strings.TrimSpace(args.GuestEmail); e != "" {
		email, err := normalizeBookerEmail(e)
		if err != nil {
			return "error: guest_email does not look like an email address. Ask the owner to check it."
		}
		d.GuestEmail = email
	}

	today := s.now.In(s.prof.Loc).Format("2006-01-02")
	horizon := s.now.In(s.prof.Loc).AddDate(1, 0, 0).Format("2006-01-02")
	okDate := func(date string) bool {
		_, err := time.ParseInLocation("2006-01-02", date, s.prof.Loc)
		return err == nil && date >= today && date <= horizon
	}

	switch {
	case args.BookAt != nil:
		minute, ok := parseClock(args.BookAt.Time)
		if !ok || !okDate(args.BookAt.Date) {
			return "error: book_at needs a date from the calendar (today or later) and an HH:MM time."
		}
		if at, _ := s.instant(args.BookAt.Date, minute); !at.After(s.now) {
			return "error: that time has already passed. Ask the owner for a later one."
		}
		if d.GuestEmail != "" {
			d.Direct, d.BookDate, d.BookMinute, d.Windows = true, args.BookAt.Date, minute, nil
		} else {
			// No email, so it cannot be booked for the guest. One window exactly the length
			// of the meeting is a link that offers that single time.
			d.Direct = false
			d.Windows = []draftWindow{{Date: args.BookAt.Date, Start: minute, End: minute + et.Minutes}}
		}
	case len(args.Windows) > 0:
		if len(args.Windows) > ownerMaxWindows {
			return fmt.Sprintf("error: at most %d windows. Ask the owner to narrow it down.", ownerMaxWindows)
		}
		var ws []draftWindow
		for _, w := range args.Windows {
			start, ok1 := parseClock(w.Start)
			end, ok2 := parseClock(w.End)
			switch {
			case !ok1 || !ok2 || !okDate(w.Date):
				return "error: each window needs a date from the calendar (today or later) and HH:MM start and end times."
			case end-start < et.Minutes:
				return fmt.Sprintf("error: the window on %s is shorter than the %d-minute meeting. Ask the owner for a longer one.", w.Date, et.Minutes)
			}
			ws = append(ws, draftWindow{Date: w.Date, Start: start, End: end})
		}
		sortWindows(ws)
		if last, _ := s.instant(ws[len(ws)-1].Date, ws[len(ws)-1].End); !last.After(s.now) {
			return "error: those times have already passed. Ask the owner for later ones."
		}
		d.Direct, d.Windows = false, ws
	case d.Direct || len(d.Windows) > 0:
		// Nothing new about the times: keep what the panel already holds.
	default:
		return "error: no days or times were given. Ask the owner when they want to offer."
	}

	if q := strings.TrimSpace(args.PlaceQuery); q != "" {
		if !et.AllowInPerson {
			return fmt.Sprintf("error: the meeting type %q has no in-person option. Tell the owner, and ask if they want a different meeting type.", et.Name)
		}
		if !strings.EqualFold(q, d.PlaceQuery) {
			d.PlaceQuery, d.Place, d.PlaceOptions, d.PlaceUnverified = q, "", nil, false
			s.lookUpPlace(ctx, d)
		}
	} else if !et.AllowInPerson {
		d.PlaceQuery, d.Place, d.PlaceOptions, d.PlaceUnverified = "", "", nil, false
	}
	if d.PlaceQuery != "" && d.Place == "" {
		d.View = "place"
	}

	if err := s.showPanel(ctx, d); err != nil {
		s.h.logger.ErrorContext(ctx, "assistant: show panel", "error", err)
		return "error: the panel could not be shown. Apologise briefly and ask the owner to try again."
	}
	return "The panel is on screen:\n" + s.draftSummary(d) + "\nTell the owner in one short sentence to check it and tap the button. Do not repeat the details."
}

// lookUpPlace fills the draft's place options from the place search. Without a search key
// the place is taken as the owner typed it, and marked so the panel says it is unchecked.
func (s *ownerSession) lookUpPlace(ctx context.Context, d *ownerDraft) {
	key := strings.TrimSpace(os.Getenv("BOOKMEET_GOOGLE_PLACES_KEY"))
	if key == "" {
		d.Place, d.PlaceUnverified = d.PlaceQuery, true
		return
	}
	places, err := searchPlaces(ctx, key, d.PlaceQuery)
	if err != nil {
		s.h.logger.WarnContext(ctx, "assistant: place search", "error", err)
		places = nil
	}
	if len(places) > 5 {
		places = places[:5]
	}
	d.PlaceOptions = places
}

// ---- the panel ----

func (s *ownerSession) draftSummary(d *ownerDraft) string {
	text, _ := s.renderPanel(d)
	return text
}

// renderPanel is the panel as text plus its buttons, for whichever view is open.
func (s *ownerSession) renderPanel(d *ownerDraft) (string, telegram.Markup) {
	et := s.typeByID(d.EventTypeID)
	var b strings.Builder
	cb := func(op string) string { return "d|" + d.ID + "|" + op }

	title := "Check the details"
	switch {
	case d.Direct:
		title = "Book this meeting?"
	case d.ReplacesLinkID != "":
		title = "Change the offer"
	}
	b.WriteString(title + "\n\n")
	guest := d.GuestName
	if d.GuestEmail != "" {
		guest += " <" + d.GuestEmail + ">"
	}
	b.WriteString("Guest: " + guest + "\n")
	if et != nil {
		b.WriteString("Meeting: " + et.label() + "\n")
	} else {
		b.WriteString("Meeting: choose a meeting type\n")
	}
	if d.Topic != "" {
		b.WriteString("Topic: " + d.Topic + "\n")
	}
	switch {
	case d.Place != "" && d.PlaceUnverified:
		b.WriteString("Where: " + d.Place + " (as you typed it, not checked)\n")
	case d.Place != "":
		b.WriteString("Where: " + d.Place + "\n")
	case d.PlaceQuery != "":
		b.WriteString("Where: not confirmed yet\n")
	}
	if d.Direct {
		end := d.BookMinute
		if et != nil {
			end += et.Minutes
		}
		fmt.Fprintf(&b, "When: %s, %s to %s (%s)\n", s.day(d.BookDate), s.clock(d.BookMinute), s.clock(end), s.zone(d.BookDate))
		fmt.Fprintf(&b, "\n%s gets a calendar invitation. No link is needed.", firstName(d.GuestName))
	} else if len(d.Windows) > 0 {
		fmt.Fprintf(&b, "Offer (%s):\n", s.zone(d.Windows[0].Date))
		for _, w := range d.Windows {
			fmt.Fprintf(&b, "  %s  %s to %s\n", s.day(w.Date), s.clock(w.Start), s.clock(w.End))
		}
		last := d.Windows[len(d.Windows)-1]
		fmt.Fprintf(&b, "Link: single use, expires %s %s", s.day(last.Date), s.clock(last.End))
	}

	back := []telegram.Button{{Text: "Back", Data: cb("m")}}
	switch d.View {
	case "type":
		b.WriteString("\n\nWhich meeting type?")
		var rows telegram.Markup
		for i, t := range s.types {
			label := t.label()
			if t.ID == d.EventTypeID {
				label = "✓ " + label
			}
			rows = append(rows, []telegram.Button{{Text: label, Data: cb(fmt.Sprintf("ty|%d", i))}})
		}
		return b.String(), append(rows, back)
	case "days":
		b.WriteString("\n\nWhich days? Tap to add or remove.")
		offered := map[string]bool{}
		for _, w := range d.Windows {
			offered[w.Date] = true
		}
		var rows telegram.Markup
		var row []telegram.Button
		for i := 0; i < ownerDaysShown; i++ {
			date := s.now.In(s.prof.Loc).AddDate(0, 0, i).Format("2006-01-02")
			label := s.day(date)
			if offered[date] {
				label = "✓ " + label
			}
			row = append(row, telegram.Button{Text: label, Data: cb("dy|" + strings.ReplaceAll(date, "-", ""))})
			if len(row) == 2 {
				rows, row = append(rows, row), nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
		return b.String(), append(rows, []telegram.Button{{Text: "Done", Data: cb("m")}})
	case "times":
		b.WriteString("\n\nWhich times, for every day offered? Different times on different days: just tell me.")
		var rows telegram.Markup
		for i, p := range timePresets {
			rows = append(rows, []telegram.Button{{Text: p.Label, Data: cb(fmt.Sprintf("tm|%d", i))}})
		}
		return b.String(), append(rows, back)
	case "place":
		var rows telegram.Markup
		switch {
		case len(d.PlaceOptions) > 0:
			fmt.Fprintf(&b, "\n\nWhich %s?", d.PlaceQuery)
			for i, p := range d.PlaceOptions {
				label := p
				if len(label) > 60 {
					label = label[:57] + "…"
				}
				rows = append(rows, []telegram.Button{{Text: label, Data: cb(fmt.Sprintf("pl|%d", i))}})
			}
		case d.PlaceQuery != "":
			fmt.Fprintf(&b, "\n\nI couldn't find %q. Tell me the place another way.", d.PlaceQuery)
		default:
			b.WriteString("\n\nTell me the name of the place.")
		}
		if d.Place != "" {
			rows = append(rows, back)
		}
		return b.String(), append(rows, []telegram.Button{{Text: "Discard", Data: cb("x")}})
	}

	var rows telegram.Markup
	if d.Direct {
		rows = append(rows, []telegram.Button{{Text: "Meeting type", Data: cb("ty")}})
		rows = append(rows, []telegram.Button{{Text: "Book it", Data: cb("go")}, {Text: "Discard", Data: cb("x")}})
		return b.String(), rows
	}
	first := []telegram.Button{{Text: "Meeting type", Data: cb("ty")}, {Text: "Days", Data: cb("dy")}, {Text: "Times", Data: cb("tm")}}
	rows = append(rows, first)
	if et != nil && et.AllowInPerson && (d.PlaceQuery != "" || d.Place != "") {
		rows = append(rows, []telegram.Button{{Text: "Place", Data: cb("pl")}})
	}
	create := "Create link"
	if d.ReplacesLinkID != "" {
		create = "Create new link"
	}
	rows = append(rows, []telegram.Button{{Text: create, Data: cb("go")}, {Text: "Discard", Data: cb("x")}})
	return b.String(), rows
}

// showPanel sends the panel as a new message at the bottom of the chat, retiring the buttons
// on any earlier copy so there is only ever one live panel to tap.
func (s *ownerSession) showPanel(ctx context.Context, d *ownerDraft) error {
	if d.MessageID != 0 {
		old := *d
		s.stripButtons(ctx, &old)
	}
	if err := s.saveDraft(ctx, d); err != nil { // assigns the id the buttons carry
		return err
	}
	text, markup := s.renderPanel(d)
	id, err := s.bot.Send(ctx, s.chatID, text, markup)
	if err != nil {
		return err
	}
	d.MessageID = id
	return s.saveDraft(ctx, d)
}

// refreshPanel redraws the panel in place after a tap.
func (s *ownerSession) refreshPanel(ctx context.Context, d *ownerDraft) {
	if err := s.saveDraft(ctx, d); err != nil {
		s.h.logger.ErrorContext(ctx, "assistant: save draft", "error", err)
		return
	}
	text, markup := s.renderPanel(d)
	if err := s.bot.Edit(ctx, d.ChatID, d.MessageID, text, markup); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: edit panel", "error", err)
	}
}

func (s *ownerSession) stripButtons(ctx context.Context, d *ownerDraft) {
	d.View = ""
	text, _ := s.renderPanel(d)
	if err := s.bot.Edit(ctx, d.ChatID, d.MessageID, text, nil); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: strip panel buttons", "error", err)
	}
}

// retirePanel closes a draft and leaves its message as a record of what it was, without
// buttons, under a line saying what became of it.
func (s *ownerSession) retirePanel(ctx context.Context, d *ownerDraft, outcome string) {
	s.closeDraft(ctx, d, "discarded")
	if d.MessageID == 0 {
		return
	}
	d.View = ""
	text, _ := s.renderPanel(d)
	if err := s.bot.Edit(ctx, d.ChatID, d.MessageID, outcome+"\n\n"+text, nil); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: retire panel", "error", err)
	}
}

// ---- taps ----

// ownerHandleCallback acts on a button tap. The tap's meaning is its callback data; no
// model reads it.
func (h *Handler) ownerHandleCallback(ctx context.Context, bot *telegram.Client, userID string, cq *telegram.CallbackQuery) {
	chatID := cq.Message.Chat.ID
	s, err := h.newOwnerSession(ctx, bot, userID, chatID)
	if err != nil {
		h.logger.ErrorContext(ctx, "assistant: session", "error", err)
		return
	}
	notice := ""
	defer func() {
		if err := bot.AnswerCallback(ctx, cq.ID, notice); err != nil {
			h.logger.WarnContext(ctx, "assistant: answer tap", "error", err)
		}
	}()

	parts := strings.Split(cq.Data, "|")
	switch {
	case len(parts) >= 3 && parts[0] == "d":
		d := s.draftByID(ctx, parts[1])
		if d == nil {
			notice = "That panel is no longer active."
			return
		}
		arg := ""
		if len(parts) > 3 {
			arg = parts[3]
		}
		notice = s.tapDraft(ctx, d, parts[2], arg)
	case len(parts) == 3 && parts[0] == "l" && parts[1] == "c":
		notice = s.tapCancelLink(ctx, parts[2], cq.Message.MessageID)
	case len(parts) == 2 && parts[0] == "l" && parts[1] == "k":
		_ = bot.Edit(ctx, chatID, cq.Message.MessageID, "Kept. The link still works.", nil)
	}
}

func (s *ownerSession) tapDraft(ctx context.Context, d *ownerDraft, op, arg string) string {
	switch op {
	case "m":
		if d.PlaceQuery != "" && d.Place == "" {
			d.View = "place"
		} else {
			d.View = ""
		}
	case "ty":
		if arg == "" {
			d.View = "type"
			break
		}
		var i int
		if _, err := fmt.Sscanf(arg, "%d", &i); err != nil || i < 0 || i >= len(s.types) {
			return ""
		}
		et := s.types[i]
		d.EventTypeID, d.View = et.ID, ""
		notice := ""
		if !et.AllowInPerson && (d.Place != "" || d.PlaceQuery != "") {
			d.Place, d.PlaceQuery, d.PlaceOptions, d.PlaceUnverified = "", "", nil, false
			notice = "That meeting type has no in-person option, so the place was removed."
		}
		// A window shorter than the new meeting could never offer a time.
		for i := range d.Windows {
			if d.Windows[i].End-d.Windows[i].Start < et.Minutes {
				d.Windows[i].End = d.Windows[i].Start + et.Minutes
			}
		}
		s.refreshPanel(ctx, d)
		return notice
	case "dy":
		if arg == "" {
			d.View = "days"
			break
		}
		if len(arg) != 8 {
			return ""
		}
		date := arg[:4] + "-" + arg[4:6] + "-" + arg[6:]
		var kept []draftWindow
		had := false
		for _, w := range d.Windows {
			if w.Date == date {
				had = true
				continue
			}
			kept = append(kept, w)
		}
		switch {
		case had && len(kept) == 0:
			return "Keep at least one day."
		case had:
			d.Windows = kept
		default:
			w := draftWindow{Date: date, Start: timePresets[0].Start, End: timePresets[0].End}
			if len(d.Windows) > 0 {
				w.Start, w.End = d.Windows[0].Start, d.Windows[0].End
			}
			if len(d.Windows) >= ownerMaxWindows {
				return "That is the most days one link can offer."
			}
			d.Windows = append(d.Windows, w)
			sortWindows(d.Windows)
		}
	case "tm":
		if arg == "" {
			d.View = "times"
			break
		}
		var i int
		if _, err := fmt.Sscanf(arg, "%d", &i); err != nil || i < 0 || i >= len(timePresets) {
			return ""
		}
		// One stretch per offered day, all the same.
		seen := map[string]bool{}
		var ws []draftWindow
		for _, w := range d.Windows {
			if !seen[w.Date] {
				seen[w.Date] = true
				ws = append(ws, draftWindow{Date: w.Date, Start: timePresets[i].Start, End: timePresets[i].End})
			}
		}
		d.Windows, d.View = ws, ""
	case "pl":
		if arg == "" {
			d.View = "place"
			break
		}
		var i int
		if _, err := fmt.Sscanf(arg, "%d", &i); err != nil || i < 0 || i >= len(d.PlaceOptions) {
			return ""
		}
		d.Place, d.PlaceUnverified, d.View = d.PlaceOptions[i], false, ""
	case "x":
		s.retirePanel(ctx, d, "Discarded.")
		s.remember(ctx, "event", "The owner discarded the panel for "+d.GuestName+".")
		return "Discarded."
	case "go":
		if d.PlaceQuery != "" && d.Place == "" {
			d.View = "place"
			s.refreshPanel(ctx, d)
			return "Pick the place first."
		}
		if d.Direct {
			return s.bookDirect(ctx, d)
		}
		return s.createLink(ctx, d)
	default:
		return ""
	}
	s.refreshPanel(ctx, d)
	return ""
}

// ---- creating and booking ----

func (s *ownerSession) linkURL(token string) string {
	return strings.TrimRight(s.h.publicURL(), "/") + "/i/" + token
}

func (s *ownerSession) placeParts(d *ownerDraft) (name, address string) {
	if d.Place == "" {
		return "", ""
	}
	name = d.Place
	if i := strings.Index(d.Place, ", "); i > 0 {
		name = d.Place[:i]
	}
	return name, d.Place
}

// createLink turns the panel into a personal link.
func (s *ownerSession) createLink(ctx context.Context, d *ownerDraft) string {
	et := s.typeByID(d.EventTypeID)
	if et == nil || len(d.Windows) == 0 {
		return "Something is missing. Tell me the meeting type and times again."
	}
	windows := make([]personallink.Window, 0, len(d.Windows))
	for _, w := range d.Windows {
		start, ok1 := s.instant(w.Date, w.Start)
		end, ok2 := s.instant(w.Date, w.End)
		if !ok1 || !ok2 {
			return "One of the days is not a date I can use. Tell me the days again."
		}
		windows = append(windows, personallink.Window{Start: start, End: end})
	}
	placeName, placeAddr := s.placeParts(d)
	params := personallink.CreateParams{
		UserID: s.userID, EventTypeID: et.ID, GuestName: d.GuestName, GuestEmail: d.GuestEmail,
		PlaceName: placeName, PlaceAddress: placeAddr, Topic: d.Topic, Windows: windows,
	}
	links := s.h.personalLinks()
	var l *personallink.Link
	var token string
	var err error
	if d.ReplacesLinkID != "" {
		l, token, err = links.Replace(ctx, s.userID, d.ReplacesLinkID, params)
	} else {
		l, token, err = links.Create(ctx, params)
	}
	switch {
	case errors.Is(err, personallink.ErrNotBookable):
		return "That link has already been used, so it can't be changed."
	case errors.Is(err, personallink.ErrInvalid):
		s.h.logger.WarnContext(ctx, "assistant: create link refused", "error", err)
		return "Those times can't be offered (they may have passed). Tell me the times again."
	case err != nil:
		s.h.logger.ErrorContext(ctx, "assistant: create link", "error", err)
		return "The link could not be made. Try again."
	}

	replaced := d.ReplacesLinkID != ""
	outcome := "Link created."
	if replaced {
		outcome = "New link created. The old one no longer works."
	}
	s.retirePanelAs(ctx, d, "done", outcome)
	s.sendLink(ctx, l, token, replaced)
	s.remember(ctx, "event", fmt.Sprintf("The owner tapped %q. A link now exists for %s: %s.", outcome, l.GuestName, s.linkLine(l)))
	return ""
}

func (s *ownerSession) retirePanelAs(ctx context.Context, d *ownerDraft, status, outcome string) {
	s.closeDraft(ctx, d, status)
	d.View = ""
	text, _ := s.renderPanel(d)
	if err := s.bot.Edit(ctx, d.ChatID, d.MessageID, outcome+"\n\n"+text, nil); err != nil {
		s.h.logger.WarnContext(ctx, "assistant: close panel", "error", err)
	}
}

// sendLink gives the owner the link and a message ready to paste to the guest.
func (s *ownerSession) sendLink(ctx context.Context, l *personallink.Link, token string, replaced bool) {
	url := s.linkURL(token)
	lead := fmt.Sprintf("Here is %s's link.", firstName(l.GuestName))
	if replaced {
		lead = fmt.Sprintf("Here is %s's new link. The old one no longer works.", firstName(l.GuestName))
	}
	exp := l.ExpiresAt.In(s.prof.Loc)
	text := fmt.Sprintf("%s It works once and expires %s %s.\n\n%s\n\nReady to paste:\nHere's a link to grab a time with me: %s",
		lead, exp.Format("Mon 2 Jan"), s.clock(exp.Hour()*60+exp.Minute()), url, url)
	s.say(ctx, text)
}

// bookDirect books the one exact time on the panel for a guest whose email the owner gave.
// It goes through the same booking core as every other booking, so the calendar is checked
// and the invitation, reminders and webhooks behave as usual.
func (s *ownerSession) bookDirect(ctx context.Context, d *ownerDraft) string {
	et := s.typeByID(d.EventTypeID)
	start, ok := s.instant(d.BookDate, d.BookMinute)
	if et == nil || !ok || d.GuestEmail == "" {
		return "Something is missing. Tell me the guest's email and the time again."
	}
	_, placeAddr := s.placeParts(d)
	b, err := s.h.createBookingForSlugWith(ctx, et.Slug, start,
		booking.Attendee{Name: d.GuestName, Email: d.GuestEmail, IANATimezone: s.prof.Loc.String()},
		nil, bookingExtras{PhysicalLocation: placeAddr})
	var answerErr *answerError
	switch {
	case errors.Is(err, errSlotUnavailable), errors.Is(err, booking.ErrDoubleBooked), errors.Is(err, errNoHostAvailable):
		s.say(ctx, fmt.Sprintf("That time isn't free on your calendar, so nothing was booked. Give me another time for %s, or I can make a link.", firstName(d.GuestName)))
		s.remember(ctx, "event", "The owner tapped Book it, but that time was not free. Nothing was booked.")
		return "Not free. Nothing booked."
	case errors.Is(err, errCalendarUnavailable):
		return "Your calendar could not be checked just now. Try again in a moment."
	case errors.As(err, &answerErr):
		s.say(ctx, "That meeting type has required questions, which I can't answer for the guest yet. Nothing was booked.")
		return "Nothing booked."
	case errors.Is(err, errPaymentRequired):
		s.say(ctx, "That meeting type takes payment, so it has to be booked on the booking page. Nothing was booked.")
		return "Nothing booked."
	case errors.Is(err, booking.ErrBookingLimitReached):
		s.say(ctx, "That guest already has the most bookings this meeting type allows. Nothing was booked.")
		return "Nothing booked."
	case err != nil:
		s.h.logger.ErrorContext(ctx, "assistant: direct booking", "error", err)
		return "The booking could not be made. Try again."
	}

	s.retirePanelAs(ctx, d, "done", "Booked.")
	var msg strings.Builder
	fmt.Fprintf(&msg, "Booked: %s\nWhen: %s, %s to %s (%s)\n", d.GuestName,
		s.day(d.BookDate), s.clock(d.BookMinute), s.clock(d.BookMinute+et.Minutes), s.zone(d.BookDate))
	if placeAddr != "" {
		msg.WriteString("Where: " + placeAddr + "\n")
	} else {
		msg.WriteString("Where: " + locationWord(b.LocationType) + "\n")
	}
	msg.WriteString("Email: " + d.GuestEmail + "\n")
	if b.InviteDelivery == booking.InviteByNone {
		msg.WriteString("It's on your calendar. This meeting type sends no invitation.")
	} else {
		fmt.Fprintf(&msg, "It's on your calendar and %s's invitation is on its way.", firstName(d.GuestName))
	}
	if tok, err := s.h.bookingSvc.IssueManageToken(ctx, b.ID); err != nil {
		s.h.logger.ErrorContext(ctx, "assistant: manage link", "error", err)
	} else {
		fmt.Fprintf(&msg, "\n\nManage link, in case you want to pass it on. %s can reschedule or cancel there:\n%s/manage/%s",
			firstName(d.GuestName), strings.TrimRight(s.h.publicURL(), "/"), tok)
	}
	s.say(ctx, msg.String())
	s.remember(ctx, "event", fmt.Sprintf("The owner tapped Book it. %s is booked for %s at %s.", d.GuestName, s.day(d.BookDate), s.clock(d.BookMinute)))
	return ""
}

// ---- existing links ----

func (s *ownerSession) linkStatus(l *personallink.Link) string {
	switch l.Status {
	case personallink.StatusOpen:
		if l.LastOpenedAt.IsZero() {
			return "open, not opened yet"
		}
		return "open, last opened " + l.LastOpenedAt.In(s.prof.Loc).Format("Mon 2 Jan 15:04")
	case personallink.StatusProposed:
		return "waiting on you: the guest suggested other times"
	case personallink.StatusFollowUp:
		return "open, the guest said they will follow up with you"
	case personallink.StatusBooked, personallink.StatusClaiming:
		return "booked"
	case personallink.StatusExpired:
		return "expired without a booking"
	case personallink.StatusCancelled:
		return "cancelled"
	default:
		return "replaced by a newer link"
	}
}

func (s *ownerSession) linkLine(l *personallink.Link) string {
	kind := "meeting"
	if et := s.typeByID(l.EventTypeID); et != nil {
		kind = et.Name
	}
	offer := ""
	if len(l.Windows) > 0 {
		first, last := l.Windows[0].Start.In(s.prof.Loc), l.Windows[len(l.Windows)-1].End.In(s.prof.Loc)
		offer = first.Format("Mon 2 Jan")
		if first.Format("2006-01-02") != last.Format("2006-01-02") {
			offer += " to " + last.Format("Mon 2 Jan")
		}
	}
	return fmt.Sprintf("%s · %s · %s · %s", l.GuestName, kind, offer, s.linkStatus(l))
}

// findLink returns the owner's most recent link for a guest name, preferring one that can
// still be used.
func (s *ownerSession) findLink(ctx context.Context, guest string) *personallink.Link {
	guest = strings.ToLower(strings.TrimSpace(guest))
	if guest == "" {
		return nil
	}
	links, err := s.h.personalLinks().List(ctx, s.userID)
	if err != nil {
		return nil
	}
	var fallback *personallink.Link
	for _, l := range links {
		name := strings.ToLower(l.GuestName)
		if !strings.Contains(name, guest) && !strings.Contains(guest, name) {
			continue
		}
		if l.Active() {
			return l
		}
		if fallback == nil {
			fallback = l
		}
	}
	return fallback
}

func (s *ownerSession) toolListLinks(ctx context.Context) string {
	links, err := s.h.personalLinks().List(ctx, s.userID)
	if err != nil {
		return "error: the links could not be read."
	}
	if len(links) == 0 {
		s.say(ctx, "You have no personal links yet.")
		return "The owner was told they have no links yet. Say nothing more."
	}
	var b strings.Builder
	b.WriteString("Your links, newest first:\n")
	for i, l := range links {
		if i == 10 {
			fmt.Fprintf(&b, "… and %d older.", len(links)-10)
			break
		}
		b.WriteString("\n" + s.linkLine(l))
	}
	s.say(ctx, b.String())
	return "The list was shown to the owner. Say nothing more unless they asked something else."
}

func (s *ownerSession) toolResendLink(ctx context.Context, l *personallink.Link) string {
	if !l.Active() {
		return fmt.Sprintf("error: %s's link is %s, so it no longer works. Offer to make a new one with change_link.", l.GuestName, s.linkStatus(l))
	}
	token, err := s.h.personalLinks().Token(ctx, s.userID, l.ID)
	if err != nil {
		return "error: the link could not be read."
	}
	s.say(ctx, fmt.Sprintf("%s's link (%s):\n\n%s\n\nReady to paste:\nHere's a link to grab a time with me: %s",
		firstName(l.GuestName), s.linkStatus(l), s.linkURL(token), s.linkURL(token)))
	return "The link was sent to the owner again. Say nothing more."
}

func (s *ownerSession) toolCancelLink(ctx context.Context, l *personallink.Link) string {
	if !l.Active() {
		return fmt.Sprintf("error: %s's link is already %s.", l.GuestName, s.linkStatus(l))
	}
	_, err := s.bot.Send(ctx, s.chatID,
		fmt.Sprintf("Cancel %s's link? They won't be able to use it.\n\n%s", firstName(l.GuestName), s.linkLine(l)),
		telegram.Markup{{{Text: "Cancel link", Data: "l|c|" + l.ID}, {Text: "Keep it", Data: "l|k"}}})
	if err != nil {
		return "error: the question could not be shown."
	}
	return "The owner was asked to confirm by tapping. Say nothing more."
}

func (s *ownerSession) tapCancelLink(ctx context.Context, linkID string, messageID int64) string {
	err := s.h.personalLinks().Cancel(ctx, s.userID, linkID)
	text := "Cancelled. The link no longer works."
	if err != nil {
		text = "That link was already used or closed, so there was nothing to cancel."
	}
	_ = s.bot.Edit(ctx, s.chatID, messageID, text, nil)
	if err == nil {
		s.remember(ctx, "event", "The owner cancelled a link by tapping.")
	}
	return ""
}

// toolChangeLink opens an existing link in the panel, so creating from it replaces the link.
func (s *ownerSession) toolChangeLink(ctx context.Context, l *personallink.Link) string {
	if l.Status == personallink.StatusBooked || l.Status == personallink.StatusClaiming {
		return fmt.Sprintf("error: %s's link has been used to book, so it can't be changed. The meeting is managed from its invitation.", l.GuestName)
	}
	if old := s.openDraft(ctx); old != nil {
		s.retirePanel(ctx, old, "Replaced by a newer panel.")
	}
	d := &ownerDraft{GuestName: l.GuestName, GuestEmail: l.GuestEmail, EventTypeID: l.EventTypeID, Topic: l.Topic, Place: l.PlaceAddress, ReplacesLinkID: l.ID}
	if d.Place != "" {
		d.PlaceQuery = l.PlaceName
	}
	// Carry over the times that have not passed; a link whose times are all gone starts
	// with none, and the owner says the new ones.
	for _, w := range l.Windows {
		if !w.End.After(s.now) {
			continue
		}
		start, end := w.Start.In(s.prof.Loc), w.End.In(s.prof.Loc)
		d.Windows = append(d.Windows, draftWindow{
			Date: start.Format("2006-01-02"), Start: start.Hour()*60 + start.Minute(), End: end.Hour()*60 + end.Minute(),
		})
	}
	if len(d.Windows) == 0 {
		return fmt.Sprintf("%s's old times have passed. Ask the owner which days and times to offer now, then call show_details with guest_name %q.", l.GuestName, l.GuestName)
	}
	if err := s.showPanel(ctx, d); err != nil {
		return "error: the panel could not be shown."
	}
	return "The panel is on screen with the link's current offer:\n" + s.draftSummary(d) + "\nAsk the owner what to change, or tell them to tap Create new link."
}

// personalLinkOwner is a tiny helper for the guest-side placeholder page.
func (h *Handler) personalLinkOwnerName(ctx context.Context, userID string) string {
	var name string
	if err := h.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, userID).Scan(&name); err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.WarnContext(ctx, "personal link: owner name", "error", err)
	}
	return name
}
