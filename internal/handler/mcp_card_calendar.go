package handler

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/calnode/calnode/internal/slots"
)

// The card's "Show my calendar": the days a link or booking is about, drawn on the card so
// the owner can see what is around the times before tapping. The owner's eyes only: the tool
// is app-only (the model cannot call it, and its result goes to the card), and even so it
// carries no calendar titles, only when the owner is busy, their usual hours and their own
// BookMeet bookings. Titles stay where they already are: the owner's Telegram messages.

const calendarMaxDays = 7

type calendarSpanIn struct {
	Start string `json:"start" jsonschema:"RFC 3339"`
	End   string `json:"end" jsonschema:"RFC 3339"`
}

type calendarDaysIn struct {
	EventType string           `json:"event_type,omitempty" jsonschema:"slug of the meeting type, for the owner's usual hours"`
	Spans     []calendarSpanIn `json:"spans" jsonschema:"the times the card is about"`
}

// calendarBlock is a stretch of one day, in minutes after the owner's midnight.
type calendarBlock struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Label string `json:"label,omitempty"`
}

type calendarDay struct {
	Date     string          `json:"date"`
	Text     string          `json:"text"`
	Hours    []calendarBlock `json:"hours"`
	Busy     []calendarBlock `json:"busy"`
	Bookings []calendarBlock `json:"bookings"`
	Asked    []calendarBlock `json:"asked"`
}

type calendarDaysOut struct {
	Zone    string        `json:"zone"`
	H24     bool          `json:"h24"`
	Checked bool          `json:"calendar_checked"`
	Days    []calendarDay `json:"days"`
}

func (h *Handler) mcpCalendarDays(ctx context.Context, req *mcp.CallToolRequest, in calendarDaysIn) (*mcp.CallToolResult, calendarDaysOut, error) {
	if !mcpDrawsCards(req) {
		return nil, calendarDaysOut{}, errors.New("the calendar view is part of BookMeet's card; this app does not show cards")
	}
	o, err := h.mcpAssistantOwner(ctx)
	if err != nil {
		return nil, calendarDaysOut{}, err
	}
	// The days the spans touch, in the owner's zone.
	var spans []slots.Interval
	days := map[string]bool{}
	for _, s := range in.Spans {
		a, errA := time.Parse(time.RFC3339, s.Start)
		z, errZ := time.Parse(time.RFC3339, s.End)
		if errA != nil || errZ != nil || !z.After(a) {
			continue
		}
		spans = append(spans, slots.Interval{Start: a, End: z})
		for d := dayStart(a, o.Loc); d.Before(z) && len(days) <= calendarMaxDays; d = d.AddDate(0, 0, 1) {
			days[d.Format("2006-01-02")] = true
		}
	}
	if len(days) == 0 {
		return nil, calendarDaysOut{}, errors.New("no days to show")
	}
	dates := make([]string, 0, len(days))
	for d := range days {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	if len(dates) > calendarMaxDays {
		dates = dates[:calendarMaxDays]
	}
	first, _ := time.ParseInLocation("2006-01-02", dates[0], o.Loc)
	last, _ := time.ParseInLocation("2006-01-02", dates[len(dates)-1], o.Loc)
	from, to := first, last.AddDate(0, 0, 1)

	out := calendarDaysOut{Zone: o.zone(), H24: o.H24}

	// Busy times from the owner's calendars, minus the events BookMeet put there itself:
	// those are drawn as bookings, from BookMeet's own records.
	var busy []slots.Interval
	if gc := h.getCal(); gc != nil {
		b, err := gc.FreeBusy(ctx, o.UserID, from, to)
		if err == nil {
			out.Checked = true
			own, _ := h.ownCalendarEvents(ctx, o.UserID, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
			busy = slots.SubtractIntervals(b, own)
		}
	}
	bookings, err := h.ownerBookingsBetween(ctx, o.UserID, from, to)
	if err != nil {
		return nil, calendarDaysOut{}, err
	}

	var etID string
	if slug := strings.TrimSpace(in.EventType); slug != "" {
		if et, _, err := h.offerType(ctx, o.UserID, slug); err == nil && et != nil {
			etID = et.ID
		}
	}
	usual := &ownerSession{h: h, userID: o.UserID, prof: ownerProfile{Name: o.Name, Loc: o.Loc, H24: o.H24}}

	for _, date := range dates {
		d0, _ := time.ParseInLocation("2006-01-02", date, o.Loc)
		day := calendarDay{Date: date, Text: d0.Format("Mon 2 Jan"),
			Hours: []calendarBlock{}, Busy: []calendarBlock{}, Bookings: []calendarBlock{}, Asked: []calendarBlock{}}
		if etID != "" {
			if hrs, err := usual.usualHours(ctx, etID, date); err == nil {
				for _, s := range hrs {
					day.Hours = append(day.Hours, calendarBlock{From: s[0], To: s[1]})
				}
			}
		}
		day.Busy = clipToDay(busy, d0, o.Loc)
		for _, b := range bookings {
			for _, blk := range clipToDay([]slots.Interval{b.iv}, d0, o.Loc) {
				blk.Label = b.label
				day.Bookings = append(day.Bookings, blk)
			}
		}
		day.Asked = clipToDay(spans, d0, o.Loc)
		out.Days = append(out.Days, day)
	}
	return nil, out, nil
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

// clipToDay is the part of each interval that falls on one of the owner's days, in minutes.
func clipToDay(ivs []slots.Interval, d0 time.Time, loc *time.Location) []calendarBlock {
	d1 := time.Date(d0.Year(), d0.Month(), d0.Day()+1, 0, 0, 0, 0, loc)
	out := []calendarBlock{}
	for _, iv := range ivs {
		a, z := iv.Start, iv.End
		if !a.Before(d1) || !z.After(d0) {
			continue
		}
		if a.Before(d0) {
			a = d0
		}
		if z.After(d1) {
			z = d1
		}
		out = append(out, calendarBlock{From: int(a.Sub(d0).Minutes()), To: int(z.Sub(d0).Minutes())})
	}
	return out
}

type ownerBooking struct {
	iv    slots.Interval
	label string
}

// ownerBookingsBetween is the owner's confirmed BookMeet bookings in a range, labelled by
// meeting type and guest: the owner's own records, the same the agent can list.
func (h *Handler) ownerBookingsBetween(ctx context.Context, userID string, from, to time.Time) ([]ownerBooking, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT b.start_at, b.end_at, COALESCE(et.name, ''),
		       COALESCE((SELECT a.name FROM booking_attendees a WHERE a.booking_id = b.id ORDER BY a.is_organizer DESC LIMIT 1), '')
		FROM bookings b
		JOIN booking_hosts bh ON bh.booking_id = b.id
		LEFT JOIN event_types et ON et.id = b.event_type_id
		WHERE bh.user_id = ? AND b.status = 'confirmed' AND b.start_at < ? AND b.end_at > ?`,
		userID, to.UTC().Format(time.RFC3339Nano), from.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ownerBooking
	for rows.Next() {
		var s, e, et, guest string
		if err := rows.Scan(&s, &e, &et, &guest); err != nil {
			return nil, err
		}
		a, errA := time.Parse(time.RFC3339Nano, s)
		z, errZ := time.Parse(time.RFC3339Nano, e)
		if errA != nil || errZ != nil {
			continue
		}
		label := et
		if guest != "" {
			label = strings.TrimSpace(et + " · " + guest)
		}
		out = append(out, ownerBooking{iv: slots.Interval{Start: a, End: z}, label: label})
	}
	return out, rows.Err()
}
