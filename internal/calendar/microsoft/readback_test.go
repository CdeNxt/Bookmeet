package microsoft

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

func TestReadEvent(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   calendar.EventState
	}{
		{"moved event reports its time", 200, `{"start":{"dateTime":"2026-11-20T19:30:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-11-20T20:00:00.0000000","timeZone":"UTC"}}`, calendar.EventPresent},
		{"cancelled meeting", 200, `{"isCancelled":true}`, calendar.EventCancelled},
		{"deleted event is only missing", 404, `{}`, calendar.EventMissing},
		{"all-day event cannot be followed", 200, `{"isAllDay":true,"start":{"dateTime":"2026-11-20T00:00:00.0000000","timeZone":"UTC"},"end":{"dateTime":"2026-11-21T00:00:00.0000000","timeZone":"UTC"}}`, calendar.EventUnknown},
		{"a non-UTC answer is not guessed at", 200, `{"start":{"dateTime":"2026-11-20T11:30:00.0000000","timeZone":"Pacific Standard Time"},"end":{"dateTime":"2026-11-20T12:00:00.0000000","timeZone":"Pacific Standard Time"}}`, calendar.EventUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t)
			connect(t, c, "u1")
			var prefer string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prefer = r.Header.Get("Prefer")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c.apiBase = srv.URL
			snap, err := c.ReadEvent(context.Background(), "u1", "", "evt-1")
			if err != nil {
				t.Fatalf("ReadEvent: %v", err)
			}
			if snap.State != tc.want {
				t.Fatalf("state = %v; want %v", snap.State, tc.want)
			}
			if prefer != `outlook.timezone="UTC"` {
				t.Errorf("Prefer = %q; the answer must be asked for in UTC", prefer)
			}
			if tc.want == calendar.EventPresent {
				want := time.Date(2026, 11, 20, 19, 30, 0, 0, time.UTC)
				if !snap.Start.Equal(want) || !snap.End.Equal(want.Add(30*time.Minute)) {
					t.Errorf("times = %v - %v", snap.Start, snap.End)
				}
			}
		})
	}
}
