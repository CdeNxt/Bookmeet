package microsoft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

// graphTZ is the UTC wall-clock format Graph expects for event start/end.
const graphTZ = "2006-01-02T15:04:05"

// graphErrBody reads a short snippet of a Graph error response so the HTTP status
// can be logged alongside Graph's own error code (e.g. "MailboxNotEnabledForRESTAPI"
// when the account has no Exchange Online mailbox). Caller still closes resp.Body.
func graphErrBody(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 600))
	return strings.TrimSpace(string(b))
}

type graphAttendee struct {
	EmailAddress struct {
		Address string `json:"address"`
		Name    string `json:"name,omitempty"`
	} `json:"emailAddress"`
	Type string `json:"type"`
}

type graphLocation struct {
	DisplayName string `json:"displayName"`
}

type graphEventReq struct {
	Subject               string          `json:"subject"`
	Body                  *graphItemBody  `json:"body,omitempty"`
	Start                 graphDateTime   `json:"start"`
	End                   graphDateTime   `json:"end"`
	Location              *graphLocation  `json:"location,omitempty"`
	Attendees             []graphAttendee `json:"attendees,omitempty"`
	IsOnlineMeeting       bool            `json:"isOnlineMeeting,omitempty"`
	OnlineMeetingProvider string          `json:"onlineMeetingProvider,omitempty"`
}

type graphItemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type graphEventResp struct {
	ID            string `json:"id"`
	OnlineMeeting *struct {
		JoinURL string `json:"joinUrl"`
	} `json:"onlineMeeting"`
}

// CreateEvent creates a Graph event on the user's default calendar and returns its
// ID and, when p.AddMeet is set, the Teams join URL. Graph emails attendees its own
// invite. Returns ("", "", nil) if the user has no is_destination connection.
func (c *Client) CreateEvent(ctx context.Context, userID string, p calendar.CreateEventParams) (string, string, string, error) {
	hc, err := c.httpClient(ctx, userID, -1, 1)
	if err != nil || hc == nil {
		return "", "", "", err
	}

	reqBody := graphEventReq{
		Subject: p.Summary,
		Start:   graphDateTime{DateTime: p.Start.UTC().Format(graphTZ), TimeZone: "UTC"},
		End:     graphDateTime{DateTime: p.End.UTC().Format(graphTZ), TimeZone: "UTC"},
	}
	if p.Description != "" {
		reqBody.Body = &graphItemBody{ContentType: "text", Content: p.Description}
	}
	if p.Location != "" {
		reqBody.Location = &graphLocation{DisplayName: p.Location}
	}
	if p.OrganizerEmail != "" {
		a := graphAttendee{Type: "required"}
		a.EmailAddress.Address = p.OrganizerEmail
		a.EmailAddress.Name = p.OrganizerName
		reqBody.Attendees = append(reqBody.Attendees, a)
	}
	if p.AddMeet {
		reqBody.IsOnlineMeeting = true
		reqBody.OnlineMeetingProvider = "teamsForBusiness"
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", "", fmt.Errorf("microsoft: create event marshal: %w", err)
	}
	// Default calendar unless the user picked a specific one inside the account. Update and
	// cancel stay on /me/events/{id}, which Graph resolves across all of the user's
	// calendars, so only creation needs to be calendar-scoped.
	createURL := c.apiBase + "/me/events"
	destCal, err := c.destinationCalendarID(ctx, userID)
	if err != nil {
		return "", "", "", err
	}
	if destCal != "" {
		createURL = c.apiBase + "/me/calendars/" + url.PathEscape(destCal) + "/events"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(body))
	if err != nil {
		return "", "", "", fmt.Errorf("microsoft: create event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("microsoft: create event call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("microsoft: create event status %d: %s", resp.StatusCode, graphErrBody(resp))
	}

	var evResp graphEventResp
	if err := json.NewDecoder(resp.Body).Decode(&evResp); err != nil {
		return "", "", "", fmt.Errorf("microsoft: create event decode: %w", err)
	}
	join := ""
	if evResp.OnlineMeeting != nil {
		join = evResp.OnlineMeeting.JoinURL
	}
	return evResp.ID, join, destCal, nil
}

// UpdateEvent moves an existing event to a new start/end (reschedule). Graph
// notifies attendees. No-op if eventID is empty or the user has no connection.
//
// calendarID is accepted for interface parity but deliberately unused: Graph addresses an
// event by id at /me/events/{id} regardless of which of the user's calendars holds it, so
// unlike Google and CalDAV there is nothing to re-target.
func (c *Client) UpdateEvent(ctx context.Context, userID, calendarID, eventID string, start, end time.Time) error {
	if eventID == "" {
		return nil
	}
	hc, err := c.httpClient(ctx, userID, -1, 1)
	if err != nil || hc == nil {
		return err
	}

	body, err := json.Marshal(struct {
		Start graphDateTime `json:"start"`
		End   graphDateTime `json:"end"`
	}{
		Start: graphDateTime{DateTime: start.UTC().Format(graphTZ), TimeZone: "UTC"},
		End:   graphDateTime{DateTime: end.UTC().Format(graphTZ), TimeZone: "UTC"},
	})
	if err != nil {
		return fmt.Errorf("microsoft: update event marshal: %w", err)
	}
	apiURL := c.apiBase + "/me/events/" + url.PathEscape(eventID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("microsoft: update event request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("microsoft: update event call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("microsoft: update event status %d", resp.StatusCode)
	}
	return nil
}

// CancelEvent deletes a Graph event by ID (Graph notifies attendees). No-op if
// eventID is empty or the user has no connection.
// calendarID is unused here for the same reason as UpdateEvent: /me/events/{id} resolves
// across all of the user's calendars.
func (c *Client) CancelEvent(ctx context.Context, userID, calendarID, eventID string) error {
	if eventID == "" {
		return nil
	}
	hc, err := c.httpClient(ctx, userID, -1, 1)
	if err != nil || hc == nil {
		return err
	}
	apiURL := c.apiBase + "/me/events/" + url.PathEscape(eventID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiURL, nil)
	if err != nil {
		return fmt.Errorf("microsoft: cancel event request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("microsoft: cancel event call: %w", err)
	}
	defer resp.Body.Close()
	// 204 = deleted; 404 = already gone — both fine.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("microsoft: cancel event status %d", resp.StatusCode)
	}
	return nil
}

// ReadEvent reads an event back so a move or deletion made in Outlook can be applied to the
// booking. Graph removes a deleted event outright, so deletion shows as a 404, reported as
// EventMissing for the caller to weigh (an event in another mailbox answers the same way).
// calendarID is unused, as for UpdateEvent.
func (c *Client) ReadEvent(ctx context.Context, userID, calendarID, eventID string) (calendar.EventSnapshot, error) {
	if eventID == "" {
		return calendar.EventSnapshot{}, nil
	}
	hc, err := c.httpClient(ctx, userID, -1, 1)
	if err != nil || hc == nil {
		return calendar.EventSnapshot{}, err
	}
	apiURL := c.apiBase + "/me/events/" + url.PathEscape(eventID) + "?$select=start,end,isCancelled,isAllDay"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return calendar.EventSnapshot{}, fmt.Errorf("microsoft: read event request: %w", err)
	}
	// Without this Graph answers in the event's own time zone, named in a Windows format.
	req.Header.Set("Prefer", `outlook.timezone="UTC"`)
	resp, err := hc.Do(req)
	if err != nil {
		return calendar.EventSnapshot{}, fmt.Errorf("microsoft: read event call: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return calendar.EventSnapshot{State: calendar.EventMissing}, nil
	default:
		return calendar.EventSnapshot{}, fmt.Errorf("microsoft: read event status %d", resp.StatusCode)
	}
	var ev struct {
		Start       graphDateTime `json:"start"`
		End         graphDateTime `json:"end"`
		IsCancelled bool          `json:"isCancelled"`
		IsAllDay    bool          `json:"isAllDay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
		return calendar.EventSnapshot{}, fmt.Errorf("microsoft: read event decode: %w", err)
	}
	if ev.IsCancelled {
		return calendar.EventSnapshot{State: calendar.EventCancelled}, nil
	}
	if ev.IsAllDay || !strings.EqualFold(ev.Start.TimeZone, "UTC") || !strings.EqualFold(ev.End.TimeZone, "UTC") {
		return calendar.EventSnapshot{}, nil
	}
	start, err1 := parseGraphUTC(ev.Start.DateTime)
	end, err2 := parseGraphUTC(ev.End.DateTime)
	if err1 != nil || err2 != nil || !end.After(start) {
		return calendar.EventSnapshot{}, nil
	}
	return calendar.EventSnapshot{State: calendar.EventPresent, Start: start, End: end}, nil
}

// parseGraphUTC parses Graph's zone-less wall clock ("2026-11-19T19:30:00.0000000") as UTC.
func parseGraphUTC(v string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04:05.9999999", v, time.UTC)
}
