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

// Cards: the Telegram details panel, drawn by Bookmeet inside the owner's AI app (MCP Apps,
// https://modelcontextprotocol.io/extensions/apps). One HTML page renders every assistant
// tool's result; its buttons call tools through the app.
//
// The owner's approval of an exception is a tap on a card, and only a tap. It reaches
// Bookmeet as owner_approve, a tool marked visibility ["app"]: the app must hide it from the
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

// mcpDrawsCards reports whether the app on this connection draws MCP Apps cards.
func mcpDrawsCards(req *mcp.CallToolRequest) bool {
	if req == nil || req.Session == nil {
		return false
	}
	p := req.Session.InitializeParams()
	if p == nil || p.Capabilities == nil {
		return false
	}
	ext, ok := p.Capabilities.Extensions[mcpUIExtension].(map[string]any)
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
	}
	HeldID int64 // approveSuggestion
	At     time.Time
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
}

// approvalResult is a tool result that asks the owner to tap: the key goes in _meta, for
// the card alone, and the text for the model says nothing is done until the tap.
func approvalResult(a *ownerApproval) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Meta: mcp.Meta{approvalKeyMeta: a.key},
		Content: []mcp.Content{&mcp.TextContent{Text: "Nothing is done yet. This needs the owner's own approval: " +
			"the owner sees a Bookmeet card with an Allow button, and only their tap can approve it. Tell the owner to tap it, or ask what they would like instead."}},
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
}

func (h *Handler) mcpOwnerApprove(ctx context.Context, req *mcp.CallToolRequest, in ownerApproveIn) (*mcp.CallToolResult, ownerApproveOut, error) {
	if !mcpDrawsCards(req) {
		return nil, ownerApproveOut{}, errors.New("approvals are made by tapping a Bookmeet card; this app does not show cards")
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
		return nil, ownerApproveOut{}, errors.New(`"Allow exceptions to my availability" is off in Bookmeet, Settings → AI, so this cannot be approved`)
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
	h.agentDid(ctx, o, fmt.Sprintf("you approved a link for %s with times outside your usual hours: %s.\n\n%s", l.GuestName, offerWords(o, l), url),
		fmt.Sprintf("the owner tapped Allow on a card and made a link for %s, offering %s, including times outside their usual hours.", l.GuestName, offerWords(o, l)))
	out := &createPersonalLinkOut{
		LinkID: l.ID, URL: url, MessageToSend: "Here's a link to grab a time with me: " + url,
		Expires: mcpTimeAt{At: rfc3339OrEmpty(l.ExpiresAt), Text: o.when(l.ExpiresAt)}, Replaced: replaces != "",
	}
	return nil, ownerApproveOut{Result: "Link created for " + l.GuestName + ".", Link: out}, nil
}

// approveDirectNow is the Telegram panel's Allow then Book it (bookDirect).
func (h *Handler) approveDirectNow(ctx context.Context, o *mcpOwner, a *ownerApproval) (*mcp.CallToolResult, ownerApproveOut, error) {
	d := a.Direct
	b, err := h.createBookingForSlugWith(ctx, d.Slug, d.Start,
		booking.Attendee{Name: d.Name, Email: d.Email, IANATimezone: o.zone()},
		nil, bookingExtras{OwnerInvited: true, OutsideRules: true})
	if err != nil {
		return nil, ownerApproveOut{}, directBookingError(o, d.Start, err)
	}
	out := h.bookedView(ctx, o, b, d.Name, d.Email)
	h.agentDid(ctx, o, fmt.Sprintf("you approved booking %s outside your hours.\nWhen: %s\n%s", d.Name, out.When.Text, out.Invitation),
		fmt.Sprintf("the owner tapped Allow on a card and booked %s for %s, outside their hours.", d.Name, out.When.Text))
	return nil, ownerApproveOut{Result: "Booked " + d.Name + " for " + out.When.Text + ".", Booked: out}, nil
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
		return nil, ownerApproveOut{}, errors.New("another Bookmeet booking starts at exactly that time, and two can't start at the same moment. Nothing was booked")
	default:
		return nil, ownerApproveOut{}, directBookingError(o, a.At, err)
	}
	h.suggestedTimeBooked(ctx, held, l, a.At, b) // tells the owner on Telegram, as the tap does
	h.rememberFor(ctx, o.UserID, "event", "Through a connected AI app, the owner tapped Book anyway on a card and booked a time "+l.GuestName+" suggested.")
	out := h.bookedView(ctx, o, b, l.GuestName, l.GuestEmail)
	return nil, ownerApproveOut{Result: "Booked " + l.GuestName + " for " + out.When.Text + ".", Booked: out}, nil
}

// ── card registration ────────────────────────────────────────────────────────

func (h *Handler) addAssistantCards(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI: mcpCardURI, Name: "bookmeet_card", Title: "Bookmeet",
		Description: "Bookmeet's card for personal links and bookings: the offer, the link, what's waiting, and the owner's approvals.",
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
		Description: "Used only by Bookmeet's card, when the owner taps Allow. Not for the model: calls that do not come from the card are refused. " +
			"To ask for an approval, call the tool the owner wanted (create_personal_link, book_for_guest or answer_guest); the card shows the Allow button.",
		Annotations: toolWrites,
		Meta:        mcp.Meta{"ui": map[string]any{"resourceUri": mcpCardURI, "visibility": []string{"app"}}},
	}, h.mcpOwnerApprove)
}

// linkAddress is a personal link's guest address.
func (h *Handler) linkAddress(token string) string {
	return strings.TrimRight(h.publicURL(), "/") + "/i/" + token
}
