package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

func TestBookingInPersonChoice(t *testing.T) {
	const address = "The Indie Alley, Bolinas Road, Fairfax, CA"
	for _, tc := range []struct {
		name         string
		allow, video bool
		status       int
		bookingType  string
	}{
		{"address only", true, false, http.StatusCreated, "in_person"},
		{"address and Google Meet", true, true, http.StatusCreated, "google_meet"},
		{"disabled", false, false, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, key, userID := setupWorkspaceWithDB(t)
			slug, eventID := seedEventTypeHTTP(t, h, key)
			if _, err := db.Exec(`UPDATE event_types SET location_type = 'google_meet' WHERE id = ?`, eventID); err != nil {
				t.Fatal(err)
			}
			settings := httptest.NewRecorder()
			req := authReq(http.MethodPatch, "/v1/event-types/"+slug, fmt.Sprintf(`{"allow_in_person":%t,"in_person_video":%t}`, tc.allow, tc.video), key)
			req.SetPathValue("slug", slug)
			h.RequireAuth(h.PatchEventType)(settings, req)
			if settings.Code != http.StatusOK {
				t.Fatalf("settings: %d %s", settings.Code, settings.Body)
			}
			if _, err := db.Exec(`INSERT INTO calendar_connections (id,user_id,provider,access_token_enc,calendar_id,is_destination)
     VALUES ('conn',?,'google','test','primary',1)`, userID); err != nil {
				t.Fatal(err)
			}

			provider := telephoneCalendar{events: make(chan calendar.CreateEventParams, 1)}
			svc := calendar.NewService(db)
			svc.Register(provider)
			h.SetCalendar(svc)

			body := fmt.Sprintf(`{"event_type_slug":%q,"start_at":"2026-06-15T09:00:00Z","name":"Test","email":"test@example.com","physical_location":%q}`, slug, address)
			rec := httptest.NewRecorder()
			h.CreateBooking(rec, httptest.NewRequest(http.MethodPost, "/v1/bookings", strings.NewReader(body)))
			if rec.Code != tc.status {
				t.Fatalf("booking: %d %s", rec.Code, rec.Body)
			}
			if rec.Code != http.StatusCreated {
				return
			}
			var response struct {
				ID      string `json:"id"`
				Type    string `json:"location_type"`
				Address string `json:"physical_location"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Type != tc.bookingType || response.Address != address {
				t.Fatalf("booking lost the selected location: %+v", response)
			}
			var stored string
			if err := db.QueryRow(`SELECT physical_location FROM bookings WHERE id = ?`, response.ID).Scan(&stored); err != nil || stored != address {
				t.Fatalf("address not persisted: %q, %v", stored, err)
			}
			select {
			case event := <-provider.events:
				if event.Location != address || event.AddMeet != tc.video || !strings.Contains(event.Description, "/manage/") {
					t.Fatalf("calendar event: location=%q, video=%v, missing manage link=%v", event.Location, event.AddMeet, !strings.Contains(event.Description, "/manage/"))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("calendar event was not created")
			}
		})
	}
}
