package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/uid"
)

// fakeRoutes stands in for Google's Routes API: every route takes `seconds`, or fails with 500.
func fakeRoutes(t *testing.T, seconds int, fail bool) *int32 {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if fail || r.Header.Get("X-Goog-Api-Key") != "test-key" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"routes":[{"duration":"%ds"}]}`, seconds)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BOOKMEET_GOOGLE_ROUTES_KEY", "test-key")
	t.Setenv("BOOKMEET_ROUTES_URL", srv.URL)
	return &calls
}

// travelSetup makes an in-person event type in the given travel mode, gives the host a home
// address, and books them at 10:00-10:30 UTC at "Office A" three days from now.
func travelSetup(t *testing.T, mode string, fixedDrive int) (slotsFor func(query string) []string, book func(hhmm, where string) int) {
	t.Helper()
	hh, database, key, owner := setupWorkspaceWithDB(t)
	slug, etID := seedEventTypeHTTP(t, hh, key)
	var day, officeA string
	calendarCheckExec(t, database, `UPDATE event_types SET allow_in_person = 1, travel_mode = ?, fixed_drive_minutes = ? WHERE id = ?`, mode, fixedDrive, etID)
	calendarCheckExec(t, database, `UPDATE users SET home_address = 'Home Base '||? WHERE id = ?`, uid.New()[:6], owner)
	day = time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02")
	d, _ := time.Parse("2006-01-02", day)
	officeA = "Office A " + uid.New()[:6]
	start, end := d.Add(10*time.Hour), d.Add(10*time.Hour+30*time.Minute)
	calendarCheckExec(t, database, `INSERT INTO bookings (id,event_type_id,host_id,start_at,end_at,status,physical_location) VALUES ('b1',?,?,?,?,'confirmed',?)`, etID, owner, start.Format(time.RFC3339), end.Format(time.RFC3339), officeA)
	calendarCheckExec(t, database, `INSERT INTO booking_hosts (booking_id,user_id) VALUES ('b1',?)`, owner)
	slotsFor = func(query string) []string {
		req := httptest.NewRequest(http.MethodGet, "/v1/event-types/"+slug+"/slots?from="+day+"&to="+day+"&tz=UTC"+query, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		hh.GetSlots(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("slots %s = %d: %s", query, rec.Code, rec.Body)
		}
		var body struct {
			Slots []struct {
				Start string `json:"start"`
			} `json:"slots"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range body.Slots {
			out = append(out, s.Start[11:16])
		}
		return out
	}
	book = func(hhmm, where string) int {
		body := fmt.Sprintf(`{"event_type_slug":%q,"start_at":"%sT%s:00Z","name":"Guest","email":"guest-%s@example.com","timezone":"UTC","physical_location":%q}`, slug, day, hhmm, uid.New()[:6], where)
		req := httptest.NewRequest(http.MethodPost, "/v1/bookings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		hh.CreateBooking(rec, req)
		return rec.Code
	}
	return slotsFor, book
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestGoogleTravelWidensBusyByTheDriveToTheGuestsAddress(t *testing.T) {
	calls := fakeRoutes(t, 30*60, false) // a 30-minute drive between any two addresses
	slotsFor, _ := travelSetup(t, "google", 0)
	loc := "&in_person=1&location=" + strings.ReplaceAll("Cafe "+uid.New()[:6], " ", "+")

	plain := slotsFor("")
	if !has(plain, "09:30") || !has(plain, "10:30") {
		t.Fatalf("without travel 09:30 and 10:30 should both be offered: %v", plain)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatalf("virtual slots must not call Google, made %d", *calls)
	}
	got := slotsFor(loc)
	for _, blocked := range []string{"09:30", "10:00", "10:30"} {
		if has(got, blocked) {
			t.Errorf("%s leaves under 30 minutes to drive from/to the other meeting: %v", blocked, got)
		}
	}
	for _, ok := range []string{"09:00", "11:00"} {
		if !has(got, ok) {
			t.Errorf("%s leaves exactly 30 minutes and should be offered: %v", ok, got)
		}
	}
	if atomic.LoadInt32(calls) == 0 {
		t.Fatal("expected a Routes lookup")
	}
}

func TestGoogleTravelFallsBackToTheFixedDriveTimeWhenGoogleFails(t *testing.T) {
	calls := fakeRoutes(t, 0, true)
	slotsFor, _ := travelSetup(t, "google", 10)
	got := slotsFor("&in_person=1&location=Fallback+Cafe+" + uid.New()[:6])
	if has(got, "09:30") || has(got, "10:30") {
		t.Errorf("a 10-minute fallback must still block 09:30 and 10:30: %v", got)
	}
	if !has(got, "09:00") || !has(got, "11:00") {
		t.Errorf("09:00 and 11:00 clear the 10-minute fallback: %v", got)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("one failure should stop further Google calls this request, made %d", n)
	}
}

func TestFixedModeNeverCallsGoogle(t *testing.T) {
	calls := fakeRoutes(t, 30*60, false)
	slotsFor, _ := travelSetup(t, "fixed", 0)
	slotsFor("&in_person=1&location=Fixed+Mode+Cafe")
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("fixed mode made %d Routes calls", n)
	}
}

// A time that was not offered must not book either: the booking applies the same drive
// time (here 30 minutes from the other meeting's address) the slot list did.
func TestGoogleTravelBookingEnforcesTheSameDriveTime(t *testing.T) {
	fakeRoutes(t, 30*60, false)
	_, book := travelSetup(t, "google", 0)
	where := "Cafe " + uid.New()[:6]
	if code := book("10:30", where); code != http.StatusConflict {
		t.Fatalf("10:30 leaves no time to drive from the 10:00-10:30 meeting: status %d, want 409", code)
	}
	if code := book("11:00", where); code != http.StatusCreated {
		t.Fatalf("11:00 leaves the full 30 minutes: status %d, want 201", code)
	}
}
