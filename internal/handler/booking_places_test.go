package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBookingPlacesStopsAtDailyLimit(t *testing.T) {
	t.Setenv("BOOKMEET_GOOGLE_PLACES_KEY", "unused-test-key")
	h, db, key, _ := setupWorkspaceWithDB(t)
	slug, id := seedEventTypeHTTP(t, h, key)
	if _, err := db.Exec(`UPDATE event_types SET allow_in_person = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bookmeet_place_search_usage (id, day, count) VALUES (1, ?, 150)`,
		time.Now().UTC().Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/event-types/"+slug+"/places", strings.NewReader(`{"query":"Fairfax Library"}`))
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.SearchBookingPlaces(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted quota should block the Google request: %d %s", rec.Code, rec.Body)
	}
}
