package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/calnode/calnode/internal/personallink"
)

// Making a personal link from an agent: the Telegram details panel's check (analyse) and its
// Create link tap (createLink), for a caller that is not the owner's own tap.
//
// What a link's windows mean is the owner's own setting ("Allow exceptions to my
// availability"), read here exactly as the Telegram panel reads it; nothing the agent passes
// can widen it. A link that would offer nothing is refused, as the Create tap refuses it.
// The approval paths that need the owner's tap (a direct booking outside hours, booking over
// a clash) are not reachable from here.

type mcpWindowIn struct {
	Date  string `json:"date" jsonschema:"the day, YYYY-MM-DD, in the owner's time zone"`
	Start string `json:"start" jsonschema:"HH:MM, 24-hour, in the owner's time zone"`
	End   string `json:"end" jsonschema:"HH:MM, 24-hour, in the owner's time zone; 24:00 is not allowed, use 23:59"`
}

type mcpOfferIn struct {
	EventType      string        `json:"event_type" jsonschema:"slug of one of the owner's meeting types (list_event_types)"`
	Windows        []mcpWindowIn `json:"windows" jsonschema:"the times to offer, one entry per day and stretch. Different days can have different times. For one exact time, give a window exactly the meeting's length"`
	ReplacesLinkID string        `json:"replaces_link_id,omitempty" jsonschema:"to change the offer on an existing link: its link_id. The old address stops working and the guest's email and phone carry over"`
}

type mcpWindowCheck struct {
	mcpTimeSpan
	// Verdict is what the engine says about this window, as things stand.
	Verdict       string `json:"verdict" jsonschema:"open, partly_outside_hours (only the part inside the owner's hours is offered), outside_hours, beyond_booking_horizon or busy_throughout"`
	VerdictText   string `json:"verdict_text"`
	TimesOffered  int    `json:"times_offered" jsonschema:"how many start times the guest would be offered in this window"`
	NeedsApproval bool   `json:"needs_owner_approval" jsonschema:"this window offers nothing unless the owner allows an exception to their own availability"`
}

type mcpOfferCheck struct {
	OwnerTimezone string           `json:"owner_timezone"`
	Today         string           `json:"today" jsonschema:"today's date in the owner's zone, YYYY-MM-DD"`
	Meeting       string           `json:"meeting"`
	Minutes       int              `json:"duration_minutes"`
	Windows       []mcpWindowCheck `json:"windows"`
	TimesOffered  int              `json:"times_offered" jsonschema:"start times offered across all windows"`
	CanCreate     bool             `json:"can_create" jsonschema:"create_personal_link will accept this offer as it is"`
	Advice        string           `json:"advice" jsonschema:"what to tell or ask the owner next"`
}

// offerType is one of the owner's meeting types, as the Telegram assistant knows them.
func (h *Handler) offerType(ctx context.Context, userID, slug string) (*ownerEventType, []string, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, slug, name, duration_minutes, location_type, allow_in_person, max_future_days
		FROM event_types WHERE user_id = ? AND is_active = 1 AND is_public = 1 ORDER BY name`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("owner event types: %w", err)
	}
	defer rows.Close()
	var found *ownerEventType
	var slugs []string
	for rows.Next() {
		var et ownerEventType
		if err := rows.Scan(&et.ID, &et.Slug, &et.Name, &et.Minutes, &et.LocationType, &et.AllowInPerson, &et.MaxFutureDays); err != nil {
			return nil, nil, err
		}
		slugs = append(slugs, et.Slug)
		if et.Slug == strings.TrimSpace(slug) {
			e := et
			found = &e
		}
	}
	return found, slugs, rows.Err()
}

// checkOffer validates the agent's windows exactly as the Telegram panel does and asks the
// engine what each would offer. Errors are worded for the agent to act on.
func (h *Handler) checkOffer(ctx context.Context, o *mcpOwner, in mcpOfferIn) (*mcpOfferCheck, *ownerEventType, []personallink.Window, error) {
	et, slugs, err := h.offerType(ctx, o.UserID, in.EventType)
	if err != nil {
		return nil, nil, nil, err
	}
	if et == nil {
		return nil, nil, nil, fmt.Errorf("event_type must be one of the owner's meeting types: %s", strings.Join(slugs, ", "))
	}
	if len(in.Windows) == 0 {
		return nil, nil, nil, fmt.Errorf("no windows were given. Ask the owner which days and times to offer")
	}
	if len(in.Windows) > ownerMaxWindows {
		return nil, nil, nil, fmt.Errorf("at most %d windows. Ask the owner to narrow it down", ownerMaxWindows)
	}
	now := time.Now()
	today := now.In(o.Loc).Format("2006-01-02")
	horizon := now.In(o.Loc).AddDate(1, 0, 0).Format("2006-01-02")
	at := func(date string, minute int) time.Time {
		d, _ := time.ParseInLocation("2006-01-02", date, o.Loc)
		return time.Date(d.Year(), d.Month(), d.Day(), minute/60, minute%60, 0, 0, o.Loc)
	}
	type win struct {
		date       string
		start, end time.Time
	}
	var ws []win
	for _, w := range in.Windows {
		date := strings.TrimSpace(w.Date)
		startMin, ok1 := parseClock(w.Start)
		endMin, ok2 := parseClock(w.End)
		if _, err := time.ParseInLocation("2006-01-02", date, o.Loc); err != nil || date < today || date > horizon {
			return nil, nil, nil, fmt.Errorf("window date %q must be YYYY-MM-DD, from today (%s) to a year ahead", w.Date, today)
		}
		if !ok1 || !ok2 {
			return nil, nil, nil, fmt.Errorf("the window on %s needs start and end as HH:MM, 24-hour", date)
		}
		if endMin-startMin < et.Minutes {
			return nil, nil, nil, fmt.Errorf("the window on %s is shorter than the %d-minute meeting. Ask the owner for a longer one", date, et.Minutes)
		}
		ws = append(ws, win{date: date, start: at(date, startMin), end: at(date, endMin)})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].start.Before(ws[j].start) })
	if !ws[len(ws)-1].end.After(now) {
		return nil, nil, nil, fmt.Errorf("those times have already passed. Ask the owner for later ones")
	}
	windows := make([]personallink.Window, len(ws))
	for i, w := range ws {
		windows[i] = personallink.Window{Start: w.start, End: w.end}
	}

	normal, exception, err := h.windowSlotCounts(ctx, et.Slug, o.zone(), windows, "")
	if err != nil {
		if errors.Is(err, errCalendarUnavailable) {
			return nil, nil, nil, fmt.Errorf("the owner's calendar could not be checked just now. Try again in a moment")
		}
		return nil, nil, nil, fmt.Errorf("check availability: %w", err)
	}
	// The owner's own setting decides what a link's windows mean, exactly as on Telegram
	// (analyse): on, a link offers every time inside its windows the owner is not busy for;
	// off, their usual hours still apply. The setting is the owner's; nothing the agent
	// passes changes it. linkException reads it again whenever the link is used.
	exceptions := h.assistantExceptions(ctx)
	out := &mcpOfferCheck{OwnerTimezone: o.zone(), Today: today, Meeting: et.Name, Minutes: et.Minutes}
	var empty []string
	for i, w := range ws {
		c := mcpWindowCheck{
			mcpTimeSpan: mcpTimeSpan{Start: rfc3339OrEmpty(w.start), End: rfc3339OrEmpty(w.end), Text: o.span(w.start, w.end)},
		}
		offered := normal[i]
		if exceptions {
			offered = exception[i]
		}
		c.TimesOffered = offered
		switch {
		case exception[i] == 0:
			c.Verdict, c.VerdictText = "busy_throughout", "the owner is busy the whole time, so nothing can be offered"
		case exceptions:
			c.Verdict, c.VerdictText = "open", "open"
		case normal[i] == 0 && et.MaxFutureDays > 0 && w.date > now.In(o.Loc).AddDate(0, 0, et.MaxFutureDays).Format("2006-01-02"):
			c.Verdict, c.VerdictText, c.NeedsApproval = "beyond_booking_horizon",
				fmt.Sprintf("beyond the %d days ahead the owner takes bookings", et.MaxFutureDays), true
		case normal[i] == 0:
			c.Verdict, c.VerdictText, c.NeedsApproval = "outside_hours", "outside the times the owner takes meetings", true
		case normal[i] < exception[i]:
			c.Verdict, c.VerdictText = "partly_outside_hours", "only the part inside the owner's hours will be offered"
		default:
			c.Verdict, c.VerdictText = "open", "open"
		}
		if offered == 0 {
			empty = append(empty, c.Text+" ("+c.VerdictText+")")
		}
		out.TimesOffered += offered
		out.Windows = append(out.Windows, c)
	}
	// Like the Create tap: refused only when the link would offer nothing at all.
	out.CanCreate = out.TimesOffered > 0
	hint := ""
	for _, c := range out.Windows {
		if c.NeedsApproval {
			hint = " Times outside the owner's usual hours can be offered only if the owner switches on \"Allow exceptions to my availability\" in Bookmeet, Settings → AI; you cannot do that for them."
			break
		}
	}
	switch {
	case !out.CanCreate:
		out.Advice = "Nothing in these windows can be offered: " + strings.Join(empty, "; ") + ". Ask the owner for other times." + hint
	case len(empty) > 0:
		out.Advice = "These windows would offer nothing: " + strings.Join(empty, "; ") + ". Tell the owner, and ask whether to go ahead with the rest or change them." + hint
	default:
		out.Advice = "Show the owner the windows as their text fields say them, with the meeting and the guest, and create the link once they agree."
	}
	return out, et, windows, nil
}

// ── check_personal_link_offer ────────────────────────────────────────────────

// It takes create_personal_link's arguments, so an agent can send the same call twice: once
// to check, once to create. The guest's name, email and topic are not needed to check.
func (h *Handler) mcpCheckPersonalLinkOffer(ctx context.Context, _ *mcp.CallToolRequest, in createPersonalLinkIn) (*mcp.CallToolResult, mcpOfferCheck, error) {
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, mcpOfferCheck{}, err
	}
	out, _, _, err := h.checkOffer(ctx, o, in.mcpOfferIn)
	if err != nil {
		return nil, mcpOfferCheck{}, err
	}
	return nil, *out, nil
}

// ── create_personal_link ─────────────────────────────────────────────────────

type createPersonalLinkIn struct {
	GuestName  string `json:"guest_name" jsonschema:"the guest's full name, as the owner said it"`
	GuestEmail string `json:"guest_email,omitempty" jsonschema:"only if the owner gave it; the guest is asked otherwise"`
	Topic      string `json:"topic,omitempty" jsonschema:"what the meeting is about, a few words in the owner's words; the guest sees it"`
	mcpOfferIn
}

type createPersonalLinkOut struct {
	LinkID        string         `json:"link_id"`
	URL           string         `json:"url" jsonschema:"the address for the owner to send the guest"`
	MessageToSend string         `json:"message_to_send" jsonschema:"a message the owner can paste to the guest"`
	Expires       mcpTimeAt      `json:"expires"`
	Replaced      bool           `json:"replaced,omitempty" jsonschema:"an older link was replaced and no longer works"`
	Offer         *mcpOfferCheck `json:"offer"`
}

func (h *Handler) mcpCreatePersonalLink(ctx context.Context, _ *mcp.CallToolRequest, in createPersonalLinkIn) (*mcp.CallToolResult, createPersonalLinkOut, error) {
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, createPersonalLinkOut{}, err
	}
	if !h.assistantEnabled(ctx) {
		return nil, createPersonalLinkOut{}, fmt.Errorf("personal links are switched off, so a new link would not open. The owner can switch the booking assistant on in Bookmeet, Settings → AI")
	}
	name := strings.TrimSpace(in.GuestName)
	if name == "" || len(name) > 200 {
		return nil, createPersonalLinkOut{}, fmt.Errorf("guest_name is missing. Ask the owner who the meeting is with")
	}
	email := ""
	if e := strings.TrimSpace(in.GuestEmail); e != "" {
		if email, err = normalizeBookerEmail(e); err != nil {
			return nil, createPersonalLinkOut{}, fmt.Errorf("guest_email does not look like an email address. Ask the owner to check it")
		}
	}
	topic := strings.TrimSpace(in.Topic)
	if len(topic) > 200 {
		topic = topic[:200]
	}
	check, et, windows, err := h.checkOffer(ctx, o, in.mcpOfferIn)
	if err != nil {
		return nil, createPersonalLinkOut{}, err
	}
	if !check.CanCreate {
		return nil, createPersonalLinkOut{}, fmt.Errorf("no link was made. %s", check.Advice)
	}

	params := personallink.CreateParams{
		UserID: o.UserID, EventTypeID: et.ID, GuestName: name, GuestEmail: email, Topic: topic, Windows: windows,
		// The owner's setting, as on Telegram (createLink); not something the agent chose.
		OutsideAvailability: h.assistantExceptions(ctx),
	}
	links := h.personalLinks()
	var l *personallink.Link
	var token string
	replaces := strings.TrimSpace(in.ReplacesLinkID)
	if replaces != "" {
		l, token, err = links.Replace(ctx, o.UserID, replaces, params)
	} else {
		l, token, err = links.Create(ctx, params)
	}
	switch {
	case errors.Is(err, personallink.ErrNotFound):
		return nil, createPersonalLinkOut{}, fmt.Errorf("no personal link with id %q to replace. Call list_personal_links to find it", replaces)
	case errors.Is(err, personallink.ErrNotBookable):
		return nil, createPersonalLinkOut{}, fmt.Errorf("that link was already used or closed, so it cannot be changed. Make a new link instead (omit replaces_link_id)")
	case errors.Is(err, personallink.ErrInvalid):
		return nil, createPersonalLinkOut{}, fmt.Errorf("those times cannot be offered (they may have passed). Ask the owner for the times again")
	case err != nil:
		return nil, createPersonalLinkOut{}, fmt.Errorf("create personal link: %w", err)
	}
	url := strings.TrimRight(h.publicURL(), "/") + "/i/" + token
	lead := "a new link for " + l.GuestName
	if replaces != "" {
		lead = "a new link for " + l.GuestName + " (the old one no longer works)"
	}
	h.agentDid(ctx, o, fmt.Sprintf("%s, %s: %s.\n\n%s", lead, et.Name, offerWords(o, l), url),
		fmt.Sprintf("the owner made %s, %s, offering %s.", lead, et.Name, offerWords(o, l)))
	return nil, createPersonalLinkOut{
		LinkID: l.ID, URL: url, MessageToSend: "Here's a link to grab a time with me: " + url,
		Expires:  mcpTimeAt{At: rfc3339OrEmpty(l.ExpiresAt), Text: o.when(l.ExpiresAt)},
		Replaced: replaces != "", Offer: check,
	}, nil
}

func (h *Handler) addAssistantCreateTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "check_personal_link_offer",
		Title: "Check a personal link offer",
		Description: "Before making a personal link: check the days and times the owner wants to offer against their availability. " +
			"Pass dates and clock times in the owner's own time zone; each window comes back with its weekday and date written out, so you can confirm with the owner that the days are right. " +
			"Takes the same arguments as create_personal_link. Says per window how many times the guest would be offered, and whether a window is outside the owner's hours, beyond how far ahead they take bookings, or busy throughout. Changes nothing.",
		Annotations: toolReadOnly,
	}, h.mcpCheckPersonalLinkOffer)

	mcp.AddTool(s, &mcp.Tool{
		Name:  "create_personal_link",
		Title: "Create a personal link",
		Description: "Make a single-use booking link for one named guest, limited to the windows given, and return its address and a message for the owner to send. " +
			"Check the offer with check_personal_link_offer and confirm the days with the owner first. " +
			"Refuses an offer with nothing in it. " +
			"To change an existing link's offer, pass replaces_link_id: the old address stops working. Never send the link to the guest yourself; the owner sends it.",
		Annotations: toolWrites,
	}, h.mcpCreatePersonalLink)
}
