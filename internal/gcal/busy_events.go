package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

type eventsListResp struct {
	Items []struct {
		Summary      string `json:"summary"`
		Status       string `json:"status"`
		Transparency string `json:"transparency"`
		Start        struct {
			DateTime string `json:"dateTime"`
			Date     string `json:"date"`
		} `json:"start"`
		End struct {
			DateTime string `json:"dateTime"`
			Date     string `json:"date"`
		} `json:"end"`
	} `json:"items"`
}

// BusyEvents names the events that block time between from and to on the calendars this
// user checks for conflicts: the same calendars FreeBusy reads. See calendar.BusyEvent for
// who may see a title.
func (c *Client) BusyEvents(ctx context.Context, userID string, from, to time.Time) ([]calendar.BusyEvent, error) {
	conns, err := c.freeBusyConnections(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []calendar.BusyEvent
	for _, conn := range conns {
		for _, calID := range conn.calIDs {
			q := url.Values{
				"timeMin":      {from.UTC().Format(time.RFC3339)},
				"timeMax":      {to.UTC().Format(time.RFC3339)},
				"singleEvents": {"true"},
				"orderBy":      {"startTime"},
				"maxResults":   {"10"},
				"fields":       {"items(summary,status,transparency,start,end)"},
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				c.apiBase+"/calendars/"+url.PathEscape(calID)+"/events?"+q.Encode(), nil)
			if err != nil {
				return nil, fmt.Errorf("gcal: events request: %w", err)
			}
			resp, err := conn.hc.Do(req)
			if err != nil {
				return nil, fmt.Errorf("gcal: events call: %w", err)
			}
			var list eventsListResp
			decodeErr := json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("gcal: events status %d", resp.StatusCode)
			}
			if decodeErr != nil {
				return nil, fmt.Errorf("gcal: events decode: %w", decodeErr)
			}
			for _, it := range list.Items {
				// Cancelled events and ones marked "free" do not block time.
				if it.Status == "cancelled" || it.Transparency == "transparent" {
					continue
				}
				start, okS := parseEventTime(it.Start.DateTime, it.Start.Date)
				end, okE := parseEventTime(it.End.DateTime, it.End.Date)
				if !okS || !okE {
					continue
				}
				title := it.Summary
				if title == "" {
					title = "(no title)"
				}
				out = append(out, calendar.BusyEvent{Title: title, Start: start, End: end})
			}
		}
	}
	return out, nil
}

// parseEventTime reads an event boundary: a timed event's dateTime, or an all-day event's date.
func parseEventTime(dateTime, date string) (time.Time, bool) {
	if dateTime != "" {
		t, err := time.Parse(time.RFC3339, dateTime)
		return t, err == nil
	}
	t, err := time.Parse("2006-01-02", date)
	return t, err == nil
}
