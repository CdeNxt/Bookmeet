package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
)

// Cards: the Telegram details panel, drawn by BookMeet inside the owner's AI app (MCP Apps,
// https://modelcontextprotocol.io/extensions/apps). One HTML page renders every assistant
// tool's result; its buttons call tools through the app.
//
// The owner's approval of an exception is a tap on a card, and only a tap. It reaches
// BookMeet as owner_approve, a tool marked visibility ["app"]: the app must hide it from the
// model and refuse it unless the card calls it. Two more locks, so a host that gets that
// wrong still cannot be talked into an approval:
//   - owner_approve takes a ticket AND a key. The key travels only in the result's _meta,
//     which MCP Apps keeps out of the model's context; the ticket is in the visible result.
//   - Tickets are issued, and owner_approve accepted, only on a connection whose app said it
//     draws cards (the io.modelcontextprotocol/ui extension). A text-only app never gets one.
//
// A ticket names one exact action, belongs to one owner, works once and expires in 15
// minutes. Spoken approval is deliberately not supported, as in ChatGPT's own voice mode: in
// voice the owner asks, and taps the card on screen.

//go:embed assets/mcp-card.html
var mcpCardHTML string

const (
	mcpCardURI       = "ui://bookmeet/card.html"
	mcpCardMIME      = "text/html;profile=mcp-app"
	mcpUIExtension   = "io.modelcontextprotocol/ui"
	approvalLifetime = 15 * time.Minute
	approvalKeyMeta  = "bookmeet/approvalKey"
)

// withCard attaches the card to a tool.
func withCard(t *mcp.Tool) *mcp.Tool {
	t.Meta = mcp.Meta{"ui": map[string]any{"resourceUri": mcpCardURI}}
	return t
}

// mcpDrawsCards reports whether the app making this request draws MCP Apps cards.
//
// /mcp over HTTP is stateless on purpose (see newMCPStreamableHandler), so this cannot come
// from a remembered session. Under protocol 2026-07-28 the app sends its capabilities with
// every request, and req.ClientCapabilities reads them; an older-protocol request on a
// stateless server has none, and is treated as text-only. That is the safe direction: such
// an app gets the text replies and refusals, never an approval ticket.
func mcpDrawsCards(req *mcp.CallToolRequest) bool {
	if req == nil {
		return false
	}
	caps := req.ClientCapabilities()
	if caps == nil {
		return false
	}
	ext, ok := caps.Extensions[mcpUIExtension].(map[string]any)
	if !ok {
		return false
	}
	switch types := ext["mimeTypes"].(type) {
	case []any:
		for _, t := range types {
			if t == mcpCardMIME {
				return true
			}
		}
	case []string:
		for _, t := range types {
			if t == mcpCardMIME {
				return true
			}
		}
	}
	return false
}

// ── approval tickets ─────────────────────────────────────────────────────────

const (
	approveLink       = "link"       // a personal link with times outside the owner's usual hours
	approveDirect     = "direct"     // one exact time outside the owner's hours, for a guest
	approveSuggestion = "suggestion" // a guest's suggested time that would not book as things stand
)

type ownerApproval struct {
	Ticket  string
	key     string
	UserID  string
	Kind    string
	Expires time.Time

	Link     personallink.CreateParams // approveLink
	Replaces string                    // approveLink: the link this one replaces, if any
	Direct   struct {                  // approveDirect
		Slug, Name, Email string
		Start             time.Time
		Shown             timeVerdict // the reasons the card showed; the tap waives only these
	}
	HeldID int64 // approveSuggestion
	At     time.Time
	Via    string // how the owner approved, when it was not a card (approvalViaEmail)
}

// tapWords is how the owner approved, for the assistant's memory.
func (a *ownerApproval) tapWords(button string) string {
	if a.Via != "" {
		return "the owner tapped " + button + " " + a.Via
	}
	return "Through a connected AI app, the owner tapped " + button + " on a card"
}

// Tickets live in memory: short-lived by design, and a restart costing the owner one more
// request is better than a table of half-made decisions.
var ownerApprovals = struct {
	sync.Mutex
	m map[string]*ownerApproval
}{m: map[string]*ownerApproval{}}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func issueApproval(a *ownerApproval) *ownerApproval {
	a.Ticket, a.key, a.Expires = randomHex(12), randomHex(16), time.Now().Add(approvalLifetime)
	ownerApprovals.Lock()
	defer ownerApprovals.Unlock()
	for id, old := range ownerApprovals.m {
		if time.Now().After(old.Expires) {
			delete(ownerApprovals.m, id)
		}
	}
	ownerApprovals.m[a.Ticket] = a
	return a
}

// takeApproval spends a ticket: it succeeds once, for its owner, with its key, in time.
func takeApproval(ticket, key, userID string) (*ownerApproval, error) {
	ownerApprovals.Lock()
	defer ownerApprovals.Unlock()
	a, ok := ownerApprovals.m[ticket]
	if !ok || a.UserID != userID || subtle.ConstantTimeCompare([]byte(a.key), []byte(key)) != 1 {
		return nil, errors.New("that approval is not valid. Ask for it again")
	}
	delete(ownerApprovals.m, ticket)
	if time.Now().After(a.Expires) {
		return nil, errors.New("that approval has expired. Ask for it again")
	}
	return a, nil
}

// mcpApprovalOut is what the card shows while it waits for the owner's tap.
type mcpApprovalOut struct {
	Ticket string   `json:"ticket" jsonschema:"for the card only"`
	Title  string   `json:"title"`
	Lines  []string `json:"lines"`
	Why    string   `json:"why"`
	Button string   `json:"button"`
	// What "Show my calendar" on the card draws: the meeting type and the times asked about.
	EventType string        `json:"event_type,omitempty"`
	Spans     []mcpTimeSpan `json:"spans,omitempty"`
}

// approvalResult is a tool result that asks the owner to tap: the key goes in _meta, for
// the card alone, and the text for the model says nothing is done until the tap.
func approvalResult(a *ownerApproval) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Meta: mcp.Meta{approvalKeyMeta: a.key},
		Content: []mcp.Content{&mcp.TextContent{Text: "Not done yet: this needs the owner's own approval. A BookMeet card with an Allow button is showing in this chat, " +
			"right with this reply. This chat is the only place to approve it: never offer Telegram or any other messaging app. Only the owner's tap on it approves; you cannot. Tell the owner, in one short line, to tap Allow on the card. " +
			"The tap may come after your reply, and the card then shows the result itself, so do not tell the owner later that nothing was done without checking first " +
			"(get_personal_link, list_personal_links or list_bookings)."}},
	}
}

// ── owner_approve (app only) ─────────────────────────────────────────────────

type ownerApproveIn struct {
	Ticket string `json:"ticket"`
	Key    string `json:"key"`
}

type ownerApproveOut struct {
	Result string                 `json:"result"`
	Link   *createPersonalLinkOut `json:"link,omitempty"`
	Booked *bookForGuestOut       `json:"booked,omitempty"`
	// Approval is set when something new got in the way since the card was drawn.
	Approval *mcpApprovalOut `json:"approval,omitempty"`
}

func (h *Handler) mcpOwnerApprove(ctx context.Context, req *mcp.CallToolRequest, in ownerApproveIn) (*mcp.CallToolResult, ownerApproveOut, error) {
	if !mcpDrawsCards(req) {
		return nil, ownerApproveOut{}, errors.New("approvals are made by tapping a BookMeet card; this app does not show cards")
	}
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, ownerApproveOut{}, err
	}
	a, err := takeApproval(in.Ticket, in.Key, o.UserID)
	if err != nil {
		return nil, ownerApproveOut{}, err
	}
	// The owner's setting is read again: switched off since the card was drawn, no exception.
	if !h.assistantExceptions(ctx) {
		return nil, ownerApproveOut{}, errors.New(`"Allow exceptions to my availability" is off in BookMeet, Settings → AI, so this cannot be approved`)
	}
	switch a.Kind {
	case approveLink:
		return h.approveLinkNow(ctx, o, a)
	case approveDirect:
		return h.approveDirectNow(ctx, o, a)
	case approveSuggestion:
		return h.approveSuggestionNow(ctx, o, a)
	}
	return nil, ownerApproveOut{}, errors.New("unknown approval")
}

// approveLinkNow is the Telegram panel's Allow then Create link (createLink).
func (h *Handler) approveLinkNow(ctx context.Context, o *mcpOwner, a *ownerApproval) (*mcp.CallToolResult, ownerApproveOut, error) {
	p := a.Link
	p.OutsideAvailability = true // the owner's tap confirmed the times outside their hours
	links := h.personalLinks()
	replaces := a.Replaces
	var l *personallink.Link
	var token string
	var err error
	if a.Link.UserID != o.UserID {
		return nil, ownerApproveOut{}, errors.New("that approval is not valid. Ask for it again")
	}
	if replaces != "" {
		l, token, err = links.Replace(ctx, o.UserID, replaces, p)
	} else {
		l, token, err = links.Create(ctx, p)
	}
	switch {
	case errors.Is(err, personallink.ErrNotBookable):
		return nil, ownerApproveOut{}, errors.New("the link being changed was already used or closed, so nothing was made")
	case errors.Is(err, personallink.ErrInvalid):
		return nil, ownerApproveOut{}, errors.New("those times can no longer be offered (they may have passed). Nothing was made")
	case err != nil:
		return nil, ownerApproveOut{}, fmt.Errorf("create personal link: %w", err)
	}
	url := h.linkAddress(token)
	h.rememberFor(ctx, o.UserID, "event", fmt.Sprintf("%s and made a link for %s, offering %s, including times outside their usual booking rules.", a.tapWords("Allow"), l.GuestName, offerWords(o, l)))
	out := &createPersonalLinkOut{
		LinkID: l.ID, URL: url, MessageToSend: "Here's a link to grab a time with me: " + url,
		Expires: mcpTimeAt{At: rfc3339OrEmpty(l.ExpiresAt), Text: o.when(l.ExpiresAt)}, Replaced: replaces != "",
	}
	return nil, ownerApproveOut{Result: "Link created for " + l.GuestName + ".", Link: out}, nil
}

// approveDirectNow books the one exact time the owner tapped "Book anyway" on. It waives
// what the card showed and nothing more: if something new is in the way since, the owner is
// shown a fresh card. Booking over something on the calendar is the same waiver as Book
// anyway on a guest's suggestion (bookPersonalLinkApproved).
func (h *Handler) approveDirectNow(ctx context.Context, o *mcpOwner, a *ownerApproval) (*mcp.CallToolResult, ownerApproveOut, error) {
	d := a.Direct
	var minutes int
	var meeting string
	if err := h.db.QueryRowContext(ctx, `SELECT name, duration_minutes FROM event_types WHERE slug = ?`, d.Slug).Scan(&meeting, &minutes); err != nil {
		return nil, ownerApproveOut{}, errors.New("that meeting type is no longer available. Nothing was booked")
	}
	v := h.timeVerdictFor(ctx, d.Slug, minutes, d.Start, "", o.UserID)
	switch {
	case v.Unknown:
		return nil, ownerApproveOut{}, errors.New("that time has passed, or your calendar could not be checked. Nothing was booked")
	case v.SameStart:
		return nil, ownerApproveOut{}, errors.New("another BookMeet booking starts at exactly that time, and two can't start at the same moment. Nothing was booked")
	case v.beyond(d.Shown):
		next := &ownerApproval{UserID: o.UserID, Kind: approveDirect}
		next.Direct = d
		next.Direct.Shown = v
		res, appr := h.directApproval(o, meeting, minutes, next)
		return res, ownerApproveOut{Result: "Not booked yet: something changed. A new card is showing.", Approval: appr}, nil
	}
	b, err := h.createBookingForSlugWith(ctx, d.Slug, d.Start,
		booking.Attendee{Name: d.Name, Email: d.Email, IANATimezone: o.zone()},
		nil, bookingExtras{OwnerInvited: true, OutsideRules: true, OwnerApproved: v.Clash, OverBookings: v.Clash})
	if err != nil {
		return nil, ownerApproveOut{}, ownerBookingError(err)
	}
	out := h.bookedView(ctx, o, b, d.Name, d.Email)
	h.rememberFor(ctx, o.UserID, "event", fmt.Sprintf("%s and booked %s for %s.", a.tapWords("Book anyway"), d.Name, out.When.Text))
	return nil, ownerApproveOut{Result: "Booked " + d.Name + " for " + out.When.Text + ".", Booked: out}, nil
}

// ownerBookingError words a booking that failed after the owner's tap, to the owner.
func ownerBookingError(err error) error {
	var answerErr *answerError
	switch {
	case errors.Is(err, booking.ErrDoubleBooked):
		return errors.New("another BookMeet booking is in the way. Nothing was booked")
	case errors.Is(err, errCalendarUnavailable):
		return errors.New("your calendar could not be checked just now. Nothing was booked; try again in a moment")
	case errors.Is(err, errSlotUnavailable), errors.Is(err, errNoHostAvailable):
		return errors.New("that time is no longer available. Nothing was booked")
	case errors.As(err, &answerErr):
		return errors.New("that meeting type has required questions for the guest, so it can't be booked from here. Nothing was booked")
	case errors.Is(err, errPaymentRequired):
		return errors.New("that meeting type takes payment, so the guest has to book it on the booking page. Nothing was booked")
	case errors.Is(err, booking.ErrEmailThrottled), errors.Is(err, booking.ErrBookingLimitReached):
		return errors.New("that guest has reached a booking limit. Nothing was booked")
	}
	return fmt.Errorf("book after approval: %w", err)
}

// approveSuggestionNow is Telegram's Book anyway on a guest's suggested time (bookSuggested
// with approved = true).
func (h *Handler) approveSuggestionNow(ctx context.Context, o *mcpOwner, a *ownerApproval) (*mcp.CallToolResult, ownerApproveOut, error) {
	links := h.personalLinks()
	held, err := links.HeldByID(ctx, o.UserID, a.HeldID)
	if err != nil || held.Outcome != "" {
		return nil, ownerApproveOut{}, errors.New("that has already been answered")
	}
	l, err := links.Get(ctx, o.UserID, held.LinkID)
	if err != nil {
		return nil, ownerApproveOut{}, errors.New("that link is no longer available")
	}
	organizer := booking.Attendee{Name: l.GuestName, Email: l.GuestEmail, IANATimezone: l.GuestTimezone, Locale: l.GuestLocale}
	b, err := h.bookPersonalLinkApproved(ctx, l, a.At, organizer)
	switch {
	case err == nil:
	case errors.Is(err, personallink.ErrNotBookable):
		return nil, ownerApproveOut{}, errors.New("that link has already been used or closed, so nothing was booked")
	case errors.Is(err, booking.ErrDoubleBooked):
		return nil, ownerApproveOut{}, errors.New("another BookMeet booking starts at exactly that time, and two can't start at the same moment. Nothing was booked")
	default:
		return nil, ownerApproveOut{}, ownerBookingError(err)
	}
	h.suggestedTimeBooked(ctx, held, l, a.At, b, false) // the card tells the owner
	h.rememberFor(ctx, o.UserID, "event", a.tapWords("Book anyway")+" and booked a time "+l.GuestName+" suggested.")
	out := h.bookedView(ctx, o, b, l.GuestName, l.GuestEmail)
	return nil, ownerApproveOut{Result: "Booked " + l.GuestName + " for " + out.When.Text + ".", Booked: out}, nil
}

// ── card registration ────────────────────────────────────────────────────────

func (h *Handler) addAssistantCards(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI: mcpCardURI, Name: "bookmeet_card", Title: "BookMeet",
		Description: "BookMeet's card for personal links and bookings: the offer, the link, what's waiting, and the owner's approvals.",
		MIMEType:    mcpCardMIME,
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: mcpCardURI, MIMEType: mcpCardMIME, Text: mcpCardHTML,
			Meta: mcp.Meta{"ui": map[string]any{"prefersBorder": true}},
		}}}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "owner_approve",
		Title: "Owner approval (card only)",
		Description: "Used only by BookMeet's card, when the owner taps Allow. Not for the model: calls that do not come from the card are refused. " +
			"To ask for an approval, call the tool the owner wanted (create_personal_link, book_for_guest or answer_guest); the card shows the Allow button.",
		Annotations: toolWrites,
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": mcpCardURI, "visibility": []string{"app"}}},
	}, h.mcpOwnerApprove)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "show_my_calendar",
		Title:       "Show my calendar (card only)",
		Description: "Used only by BookMeet's card, for its Show my calendar button: the owner's busy times, usual hours and BookMeet bookings on the days a card is about. Not for the model: calls that do not come from the card are refused.",
		Annotations: toolReadOnly,
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": mcpCardURI, "visibility": []string{"app"}}},
	}, h.mcpCalendarDays)
}

// linkAddress is a personal link's guest address.
func (h *Handler) linkAddress(token string) string {
	return strings.TrimRight(h.publicURL(), "/") + "/i/" + token
}
