package handler

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	Start string `json:"start,omitempty" jsonschema:"HH:MM, 24-hour, in the owner's time zone. Omit start and end when the owner named the day but no times: the owner's usual hours that day are used"`
	End   string `json:"end,omitempty" jsonschema:"HH:MM, 24-hour, in the owner's time zone; 24:00 is not allowed, use 23:59. Omit with start for the owner's usual hours"`
}

type mcpOfferIn struct {
	EventType      string        `json:"event_type" jsonschema:"slug of one of the owner's meeting types (list_event_types)"`
	Windows        []mcpWindowIn `json:"windows" jsonschema:"the times to offer, one entry per day and stretch. Different days can have different times. For one exact time, give a window exactly the meeting's length"`
	ReplacesLinkID string        `json:"replaces_link_id,omitempty" jsonschema:"to change the offer on an existing link: its link_id. The old address stops working and the guest's email and phone carry over"`
	PlaceQuery     string        `json:"place_query,omitempty" jsonschema:"for an in-person meeting: the place as the owner named it (\"Equator Coffee on Mission\"). The check looks it up and returns place_options"`
	Place          string        `json:"place,omitempty" jsonschema:"for an in-person meeting: the option the owner picked, copied exactly from place_options. Never write an address yourself"`
	AskOwner       bool          `json:"ask_owner,omitempty" jsonschema:"only for apps that show no BookMeet cards, ignored otherwise. Create only: when some times are outside the owner's usual hours, send the owner the details on Telegram to confirm with their own tap. Only after the owner says yes to asking"`
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
	OwnerTimezone   string           `json:"owner_timezone"`
	Today           string           `json:"today" jsonschema:"today's date in the owner's zone, YYYY-MM-DD"`
	Meeting         string           `json:"meeting"`
	Minutes         int              `json:"duration_minutes"`
	Windows         []mcpWindowCheck `json:"windows"`
	TimesOffered    int              `json:"times_offered" jsonschema:"start times offered across all windows"`
	CanCreate       bool             `json:"can_create" jsonschema:"create_personal_link will accept this offer as it is"`
	NeedsOwner      bool             `json:"needs_owner_approval" jsonschema:"some times are outside the owner's usual hours: only the owner can confirm them, with their own tap (create_personal_link with ask_owner)"`
	SkippedDays     []string         `json:"skipped_days,omitempty" jsonschema:"days given without times that the owner does not usually work, left out"`
	PlaceOptions    []string         `json:"place_options,omitempty" jsonschema:"places found for place_query; show them to the owner and pass the one they pick as place"`
	Place           string           `json:"place,omitempty" jsonschema:"the confirmed place the link will carry"`
	PlaceUnverified bool             `json:"place_unverified,omitempty" jsonschema:"this server has no place search, so the place is taken as typed and unchecked"`
	Advice          string           `json:"advice" jsonschema:"what to tell or ask the owner next"`
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
		date             string
		startMin, endMin int
		start, end       time.Time
	}
	var ws []win
	var skipped []string
	usual := &ownerSession{h: h, userID: o.UserID, prof: ownerProfile{Name: o.Name, Loc: o.Loc, H24: o.H24}}
	for _, w := range in.Windows {
		date := strings.TrimSpace(w.Date)
		if _, err := time.ParseInLocation("2006-01-02", date, o.Loc); err != nil || date < today || date > horizon {
			return nil, nil, nil, fmt.Errorf("window date %q must be YYYY-MM-DD, from today (%s) to a year ahead", w.Date, today)
		}
		if strings.TrimSpace(w.Start) == "" && strings.TrimSpace(w.End) == "" {
			// Days without times: the owner's usual hours that day, and none on a day off,
			// exactly as the Telegram panel fills them (usualHours).
			spans, err := usual.usualHours(ctx, et.ID, date)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("the owner's usual hours could not be read. Ask the owner which times to offer")
			}
			if len(spans) == 0 {
				skipped = append(skipped, at(date, 12*60).Format("Mon 2 Jan"))
			}
			for _, sp := range spans {
				if sp[1]-sp[0] >= et.Minutes {
					ws = append(ws, win{date: date, startMin: sp[0], endMin: sp[1], start: at(date, sp[0]), end: at(date, sp[1])})
				}
			}
			continue
		}
		startMin, ok1 := parseClock(w.Start)
		endMin, ok2 := parseClock(w.End)
		if !ok1 || !ok2 {
			return nil, nil, nil, fmt.Errorf("the window on %s needs start and end as HH:MM, 24-hour, or neither for the owner's usual hours", date)
		}
		if endMin-startMin < et.Minutes {
			return nil, nil, nil, fmt.Errorf("the window on %s is shorter than the %d-minute meeting. Ask the owner for a longer one", date, et.Minutes)
		}
		ws = append(ws, win{date: date, startMin: startMin, endMin: endMin, start: at(date, startMin), end: at(date, endMin)})
	}
	if len(ws) == 0 && len(skipped) > 0 {
		return nil, nil, nil, fmt.Errorf("none of those days are among the owner's usual working days (%s). Ask the owner whether they want to offer them anyway, and at what times", strings.Join(skipped, ", "))
	}
	if len(ws) > ownerMaxWindows {
		return nil, nil, nil, fmt.Errorf("that is more than %d stretches of time. Ask the owner to narrow the days down", ownerMaxWindows)
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].start.Before(ws[j].start) })
	if !ws[len(ws)-1].end.After(now) {
		return nil, nil, nil, fmt.Errorf("those times have already passed. Ask the owner for later ones")
	}
	windows := make([]personallink.Window, len(ws))
	for i, w := range ws {
		windows[i] = personallink.Window{Start: w.start, End: w.end}
	}

	// In person, as on the Telegram panel: the place is looked up, the owner picks one of
	// the results, and the address always comes from the search, never from the model.
	var placeOptions []string
	place, placeUnverified := "", false
	if q := strings.TrimSpace(in.PlaceQuery); q != "" {
		if !et.AllowInPerson {
			return nil, nil, nil, fmt.Errorf("the meeting type %q has no in-person option. Tell the owner, and ask whether they want a different meeting type", et.Name)
		}
		if key := strings.TrimSpace(os.Getenv("BOOKMEET_GOOGLE_PLACES_KEY")); key == "" {
			place, placeUnverified = q, true
		} else {
			found, err := searchPlaces(ctx, key, q)
			if err != nil {
				h.logger.WarnContext(ctx, "mcp: place search", "error", err)
			}
			if len(found) > 5 {
				found = found[:5]
			}
			placeOptions = found
			for _, p := range found {
				if p == strings.TrimSpace(in.Place) {
					place = p
				}
			}
		}
	} else if strings.TrimSpace(in.Place) != "" {
		return nil, nil, nil, fmt.Errorf("place must come from place_options: pass place_query as well, with the place as the owner named it")
	}

	normal, exception, err := h.windowSlotCounts(ctx, et.Slug, o.zone(), windows, place)
	if err != nil {
		if errors.Is(err, errCalendarUnavailable) {
			return nil, nil, nil, fmt.Errorf("the owner's calendar could not be checked just now. Try again in a moment")
		}
		return nil, nil, nil, fmt.Errorf("check availability: %w", err)
	}
	// The same rules as the Telegram panel (analyse and the Create tap): times outside the
	// owner's usual hours are flagged, because the owner asked for them explicitly or not at
	// all (days without times got the usual hours). With the exceptions setting on, they
	// need the owner's own confirmation before a link is made, and then the link offers its
	// whole windows. With it off, the owner's hours decide and only the inside part is
	// offered. Nothing the agent passes changes either.
	exceptions := h.assistantExceptions(ctx)
	out := &mcpOfferCheck{OwnerTimezone: o.zone(), Today: today, Meeting: et.Name, Minutes: et.Minutes, SkippedDays: skipped}
	var empty []string
	rules, err := h.loadBookableEventType(ctx, et.Slug)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load meeting type: %w", err)
	}
	for i, w := range ws {
		// A window inside the owner's hours that only the minimum notice cuts into is not
		// "outside your hours", and is not worded that way.
		shortNotice := false
		if normal[i] < exception[i] {
			if shortNotice, err = h.onlyShortNotice(ctx, rules, o.UserID, windows[i]); err != nil {
				return nil, nil, nil, fmt.Errorf("check availability: %w", err)
			}
		}
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
		case shortNotice:
			c.Verdict, c.VerdictText = "short_notice", "sooner than the "+noticeWords(rules.MinNoticeMinutes)+" minimum notice the owner requires, in full or in part"
		case normal[i] == 0 && et.MaxFutureDays > 0 && w.date > now.In(o.Loc).AddDate(0, 0, et.MaxFutureDays).Format("2006-01-02"):
			c.Verdict, c.VerdictText = "beyond_booking_horizon", fmt.Sprintf("beyond the %d days ahead the owner takes bookings", et.MaxFutureDays)
		case normal[i] == 0:
			c.Verdict, c.VerdictText = "outside_hours", "outside the times the owner takes meetings"
		case normal[i] < exception[i]:
			c.Verdict, c.VerdictText = "partly_outside_hours", "only part of this is inside the times the owner takes meetings"
		default:
			c.Verdict, c.VerdictText = "open", "open"
		}
		outside := c.Verdict == "short_notice" || c.Verdict == "outside_hours" || c.Verdict == "beyond_booking_horizon" || c.Verdict == "partly_outside_hours"
		c.NeedsApproval = outside && exceptions
		if !exceptions && c.Verdict == "partly_outside_hours" {
			c.VerdictText = "only the part inside the owner's hours will be offered"
		}
		if c.NeedsApproval {
			out.NeedsOwner = true
		}
		if offered == 0 {
			empty = append(empty, c.Text+" ("+c.VerdictText+")")
		}
		out.TimesOffered += offered
		out.Windows = append(out.Windows, c)
	}
	out.PlaceOptions, out.Place, out.PlaceUnverified = placeOptions, place, placeUnverified
	// Like the Create tap: refused when the link would offer nothing at all, or when an
	// in-person place has not been picked yet.
	out.CanCreate = out.TimesOffered > 0 && !out.NeedsOwner
	needsPlace := strings.TrimSpace(in.PlaceQuery) != "" && place == ""
	hint := ""
	if !exceptions {
		for _, c := range out.Windows {
			if c.Verdict == "outside_hours" || c.Verdict == "beyond_booking_horizon" {
				hint = " The owner keeps links to their usual hours (\"Allow exceptions to my availability\" is off in BookMeet, Settings → AI), so times outside them are not offered; only the owner can change that."
				break
			}
		}
	}
	switch {
	case needsPlace && len(placeOptions) == 0:
		out.CanCreate = false
		out.Advice = "No place was found for that place_query. Ask the owner to name it more fully (the business and the street or town)."
	case needsPlace:
		out.CanCreate = false
		out.Advice = "Show the owner the place_options and ask which one; then pass it, copied exactly, as place."
	case out.NeedsOwner && out.TimesOffered > 0:
		out.Advice = "Some of these times are outside the owner's usual hours (see verdict_text). Tell the owner which; if they want them anyway, call create_personal_link with ask_owner set to true and they confirm on Telegram with their own tap. Or change the times."
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

// checkPersonalLinkOfferIn is create_personal_link's arguments with nothing about the guest
// required, so an agent can send the same call twice (once to check, once to create) or
// check times before it knows who the guest is.
type checkPersonalLinkOfferIn struct {
	GuestName  string `json:"guest_name,omitempty" jsonschema:"not needed to check"`
	GuestEmail string `json:"guest_email,omitempty" jsonschema:"not needed to check"`
	Topic      string `json:"topic,omitempty" jsonschema:"not needed to check"`
	mcpOfferIn
}

func (h *Handler) mcpCheckPersonalLinkOffer(ctx context.Context, _ *mcp.CallToolRequest, in checkPersonalLinkOfferIn) (*mcp.CallToolResult, mcpOfferCheck, error) {
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
	// Approval is set instead of a link when only the owner's tap on the card can confirm it.
	Approval *mcpApprovalOut `json:"approval,omitempty" jsonschema:"no link is made yet: the owner approves on the card"`
}

func (h *Handler) mcpCreatePersonalLink(ctx context.Context, req *mcp.CallToolRequest, in createPersonalLinkIn) (*mcp.CallToolResult, createPersonalLinkOut, error) {
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, createPersonalLinkOut{}, err
	}
	if !h.assistantEnabled(ctx) {
		return nil, createPersonalLinkOut{}, fmt.Errorf("personal links are switched off, so a new link would not open. The owner can switch the booking assistant on in BookMeet, Settings → AI")
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
	if !check.CanCreate && check.NeedsOwner && check.TimesOffered > 0 && mcpDrawsCards(req) {
		// The owner confirms the times outside their usual hours with a tap on the card.
		placeName, placeAddr := splitPlace(check.Place)
		ap := issueApproval(&ownerApproval{UserID: o.UserID, Kind: approveLink, Replaces: strings.TrimSpace(in.ReplacesLinkID),
			Link: personallink.CreateParams{UserID: o.UserID, EventTypeID: et.ID, GuestName: name, GuestEmail: email, Topic: topic,
				PlaceName: placeName, PlaceAddress: placeAddr, Windows: windows}})
		lines := []string{et.Name}
		var outside []string
		onlyNotice := true
		var spans []mcpTimeSpan
		for _, w := range check.Windows {
			lines = append(lines, w.Text)
			spans = append(spans, w.mcpTimeSpan)
			if w.NeedsApproval {
				outside = append(outside, w.Text)
				onlyNotice = onlyNotice && w.Verdict == "short_notice"
			}
		}
		title, why := "Link for "+name+" with times outside your hours?", "Outside your usual hours: "
		if onlyNotice {
			title, why = "Link for "+name+" with short-notice times?", "Sooner than your minimum notice: "
		}
		return approvalResult(ap), createPersonalLinkOut{Offer: check, Approval: &mcpApprovalOut{
			Ticket: ap.Ticket, Title: title, Lines: lines,
			Why: why + strings.Join(outside, "; ") + ".", Button: "Allow and create link",
			EventType: et.Slug, Spans: spans,
		}}, nil
	}
	if !check.CanCreate && check.NeedsOwner && check.TimesOffered > 0 && in.AskOwner && !mcpDrawsCards(req) {
		return nil, createPersonalLinkOut{}, errors.New(h.askOwnerToConfirmLink(ctx, o, et, name, email, topic, check, windows, strings.TrimSpace(in.ReplacesLinkID)))
	}
	if !check.CanCreate {
		return nil, createPersonalLinkOut{}, fmt.Errorf("no link was made. %s", check.Advice)
	}

	placeName, placeAddr := splitPlace(check.Place)
	params := personallink.CreateParams{
		UserID: o.UserID, EventTypeID: et.ID, GuestName: name, GuestEmail: email, Topic: topic, Windows: windows,
		PlaceName: placeName, PlaceAddress: placeAddr,
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
	h.agentDid(ctx, req, o, fmt.Sprintf("%s, %s: %s.\n\n%s", lead, et.Name, offerWords(o, l), url),
		fmt.Sprintf("the owner made %s, %s, offering %s.", lead, et.Name, offerWords(o, l)))
	return nil, createPersonalLinkOut{
		LinkID: l.ID, URL: url, MessageToSend: "Here's a link to grab a time with me: " + url,
		Expires:  mcpTimeAt{At: rfc3339OrEmpty(l.ExpiresAt), Text: o.when(l.ExpiresAt)},
		Replaced: replaces != "", Offer: check,
	}, nil
}

func (h *Handler) addAssistantCreateTools(s *mcp.Server) {
	mcp.AddTool(s, withCard(&mcp.Tool{
		Name:  "check_personal_link_offer",
		Title: "Check a personal link offer",
		Description: "Preview a personal link before making it: check the days and times the owner wants to offer against their availability. " +
			"Use it when the owner wants to see first or the days are unclear; when the guest and times are clear, call create_personal_link directly, which runs the same check. " +
			"Pass guest_name when you know it: in apps that show BookMeet's card, the card then has the button that creates the link. " +
			"Pass dates and clock times in the owner's own time zone; each window comes back with its weekday and date written out, so you can confirm with the owner that the days are right. " +
			"Takes the same arguments as create_personal_link. Says per window how many times the guest would be offered, and whether a window is outside the owner's hours, beyond how far ahead they take bookings, or busy throughout. Changes nothing.",
		Annotations: toolReadOnly,
	}), h.mcpCheckPersonalLinkOffer)

	mcp.AddTool(s, withCard(&mcp.Tool{
		Name:  "create_personal_link",
		Title: "Create a personal link",
		Description: "Make a single-use booking link for one named guest, limited to the windows given, and return its address and a message for the owner to send. " +
			"When the owner has named the guest and the days and times, call it directly: it checks the offer itself. If any day is unclear, check_personal_link_offer first and confirm with the owner. " +
			"If some times are outside the owner's usual hours, an app that shows BookMeet's card gets an approval card with an Allow button in the chat: that tap is the owner's approval, so do not ask them to approve in words first. " +
			"Refuses an offer with nothing in it. " +
			"For an in-person meeting, pass place_query, show the owner the place_options the check returns, and pass the one they pick as place. " +
			"To change an existing link's offer, pass replaces_link_id: the old address stops working. Never send the link to the guest yourself; the owner sends it.",
		Annotations: toolWrites,
	}), h.mcpCreatePersonalLink)
}

// askOwnerToConfirmLink puts the Telegram details panel for this link in front of the owner,
// with its Allow and Create link buttons, because some of its times are outside their usual
// hours and only they can confirm that. The agent's call makes nothing; the owner's taps do.
func (h *Handler) askOwnerToConfirmLink(ctx context.Context, o *mcpOwner, et *ownerEventType, name, email, topic string, check *mcpOfferCheck, windows []personallink.Window, replaces string) string {
	if h.ownerTelegramSession(ctx, o.UserID) == nil {
		return "no link was made, and the owner has no Telegram assistant connected to confirm times outside their usual hours with. Offer times inside their hours instead"
	}
	draft := make([]draftWindow, 0, len(windows))
	for _, w := range windows {
		s, e := w.Start.In(o.Loc), w.End.In(o.Loc)
		draft = append(draft, draftWindow{Date: s.Format("2006-01-02"), Start: s.Hour()*60 + s.Minute(), End: e.Hour()*60 + e.Minute()})
	}
	h.assistantRun(o.UserID, func(ctx context.Context) {
		s := h.ownerTelegramSession(ctx, o.UserID)
		if s == nil {
			return
		}
		if old := s.openDraft(ctx); old != nil {
			s.retirePanel(ctx, old, "Replaced by a newer panel.")
		}
		d := &ownerDraft{GuestName: name, GuestEmail: email, EventTypeID: et.ID, Topic: topic, Windows: draft,
			Place: check.Place, PlaceUnverified: check.PlaceUnverified, ReplacesLinkID: replaces}
		if check.Place != "" {
			d.PlaceQuery = check.Place
		}
		s.analyse(ctx, d)
		s.say(ctx, fmt.Sprintf("Your AI app asked for a link for %s with some times outside your usual hours. Tap Allow, then Create link, if you want it.", name))
		if err := s.showPanel(ctx, d); err != nil {
			h.logger.ErrorContext(ctx, "mcp: show panel to owner", "error", err)
		}
		s.remember(ctx, "event", fmt.Sprintf("Through a connected AI app, the owner asked for a link for %s with times outside their usual hours. The details panel is waiting for their tap.", name))
	})
	return "no link is made yet. Some times are outside the owner's usual hours, so the owner has been sent the details on Telegram to confirm with their own tap. list_personal_links will show the link once they do; they get the address there"
}

// splitPlace is a confirmed place as name and address: the same split the Telegram panel
// makes (placeParts).
func splitPlace(place string) (name, address string) {
	if place == "" {
		return "", ""
	}
	name = place
	if i := strings.Index(place, ", "); i > 0 {
		name = place[:i]
	}
	return name, place
}
