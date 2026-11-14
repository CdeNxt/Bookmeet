package gcal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// BusyEvents names what blocks a time, and leaves out what does not: an event the host
// cancelled, and one they marked as "free".
func TestBusyEvents_namesWhatBlocksTheTime(t *testing.T) {
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[
			{"summary":"Dentist","status":"confirmed","start":{"dateTime":"2026-06-15T13:30:00Z"},"end":{"dateTime":"2026-06-15T14:30:00Z"}},
			{"summary":"Old plan","status":"cancelled","start":{"dateTime":"2026-06-15T13:00:00Z"},"end":{"dateTime":"2026-06-15T14:00:00Z"}},
			{"summary":"Focus time","status":"confirmed","transparency":"transparent","start":{"dateTime":"2026-06-15T13:00:00Z"},"end":{"dateTime":"2026-06-15T15:00:00Z"}},
			{"status":"confirmed","start":{"dateTime":"2026-06-15T14:00:00Z"},"end":{"dateTime":"2026-06-15T14:15:00Z"}},
			{"summary":"Conference","status":"confirmed","start":{"date":"2026-06-15"},"end":{"date":"2026-06-16"}}
		]}`))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t)
	c.apiBase = srv.URL
	saveAndConnectClient(t, c, "user-1", "primary", "test-access-token")

	from := time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
	events, err := c.BusyEvents(context.Background(), "user-1", from, from.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("BusyEvents: %v", err)
	}
	var titles []string
	for _, e := range events {
		titles = append(titles, e.Title)
	}
	if got, want := strings.Join(titles, " | "), "Dentist | (no title) | Conference"; got != want {
		t.Fatalf("events = %q; want %q", got, want)
	}
	if !events[0].Start.Equal(time.Date(2026, 6, 15, 13, 30, 0, 0, time.UTC)) {
		t.Errorf("Dentist starts %v", events[0].Start)
	}
	if path != "/calendars/primary/events" || !strings.Contains(query, "singleEvents=true") || !strings.Contains(query, "timeMin=2026-06-15T14%3A00%3A00Z") {
		t.Errorf("request was %s?%s", path, query)
	}
}

// A calendar that cannot be read is an error to the caller, who treats it as "nothing to name".
func TestBusyEvents_failureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t)
	c.apiBase = srv.URL
	saveAndConnectClient(t, c, "user-1", "primary", "test-access-token")
	if _, err := c.BusyEvents(context.Background(), "user-1", time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("a refused request was reported as no events")
	}
}
