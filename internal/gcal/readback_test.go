package gcal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

func readEventServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s; want GET", r.Method)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A host drags the event to another time in Google Calendar, deletes it, or turns it into an
// all-day event: ReadEvent reports each so the booking can follow (or be left alone).
func TestReadEvent(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   calendar.EventState
	}{
		{"moved event reports its time", 200, `{"status":"confirmed","start":{"dateTime":"2026-11-20T11:30:00-08:00"},"end":{"dateTime":"2026-11-20T12:00:00-08:00"}}`, calendar.EventPresent},
		{"deleted event stays readable as cancelled", 200, `{"status":"cancelled"}`, calendar.EventCancelled},
		{"not found is only missing, never cancelled", 404, `{}`, calendar.EventMissing},
		{"gone is only missing", 410, `{}`, calendar.EventMissing},
		{"all-day event cannot be followed", 200, `{"status":"confirmed","start":{"date":"2026-11-20"},"end":{"date":"2026-11-21"}}`, calendar.EventUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t)
			c.apiBase = readEventServer(t, tc.status, tc.body).URL
			saveDestinationConnection(t, c, "user-1", "primary")
			snap, err := c.ReadEvent(context.Background(), "user-1", "", "evt-1")
			if err != nil {
				t.Fatalf("ReadEvent: %v", err)
			}
			if snap.State != tc.want {
				t.Fatalf("state = %v; want %v", snap.State, tc.want)
			}
			if tc.want == calendar.EventPresent {
				want := time.Date(2026, 11, 20, 19, 30, 0, 0, time.UTC)
				if !snap.Start.Equal(want) || !snap.End.Equal(want.Add(30*time.Minute)) {
					t.Errorf("times = %v - %v; want %v + 30m", snap.Start, snap.End, want)
				}
			}
		})
	}
}

func TestReadEvent_serverErrorAndNoConnection(t *testing.T) {
	c := newTestClient(t)
	if snap, err := c.ReadEvent(context.Background(), "nobody", "", "evt-1"); err != nil || snap.State != calendar.EventUnknown {
		t.Errorf("no connection: %v %v; want unknown, nil", snap.State, err)
	}
	c.apiBase = readEventServer(t, 500, `{}`).URL
	saveDestinationConnection(t, c, "user-1", "primary")
	if _, err := c.ReadEvent(context.Background(), "user-1", "", "evt-1"); err == nil {
		t.Error("a 500 must be an error, not a verdict on the event")
	}
}
