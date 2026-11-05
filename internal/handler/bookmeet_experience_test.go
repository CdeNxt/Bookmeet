package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBookMeetExperiencePersistsAndRenders(t *testing.T) {
	h, key, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, key)
	patch := func(body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.RequireAuth(h.PatchBranding)(rec, authReq(http.MethodPatch, "/v1/settings/branding", body, key))
		if rec.Code != want {
			t.Fatalf("patch %d: %s", rec.Code, rec.Body.String())
		}
	}
	page := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
		req.SetPathValue("slug", slug)
		h.BookPage(rec, req)
		if rec.Code != 200 {
			t.Fatalf("page %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	patch(`{"booking_effect":"unicorns","cancel_effect":"off","hide_attribution":true}`, 200)
	// Updating an unrelated field must preserve the experience settings.
	patch(`{"business_name":"BookMeet"}`, 200)
	body := page()
	if !strings.Contains(body, `data-booked="unicorns" data-cancelled="off"`) || strings.Contains(body, `href="https://calnode.com"`) {
		t.Fatal("experience not persisted in rendered page")
	}
	patch(`{"booking_effect":"<script>bad</script>"}`, 400)
	patch(`{"cancel_effect":"unicorns"}`, 400)
	patch(`{"booking_effect":"off","cancel_effect":"sad","hide_attribution":false}`, 200)
	body = page()
	if !strings.Contains(body, `data-booked="off" data-cancelled="sad"`) || !strings.Contains(body, `href="https://calnode.com"`) {
		t.Fatal("experience switches did not update rendered page")
	}
}

func TestBookMeetCopyAndAppearance(t *testing.T) {
	h, key, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, key)
	patch := func(body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.RequireAuth(h.PatchBranding)(rec, authReq(http.MethodPatch, "/v1/settings/branding", body, key))
		if rec.Code != want {
			t.Fatalf("patch %d: %s", rec.Code, rec.Body.String())
		}
	}
	patch(`{"public_theme":"sepia"}`, 400)
	patch(`{"public_copy":{"unknown":"ignored?"}}`, 400)
	patch(`{"public_copy":{"booking_title":"`+strings.Repeat("a", 501)+`"}}`, 400)
	patch(`{"public_theme":"light","public_copy":{"booking_title":"See you <soon> & bring ideas!","manage_subtitle":""}}`, 200)
	patch(`{"public_copy":{"nav_note":"A personal hello"}}`, 200)
	// A later unrelated settings save preserves both the appearance and earlier copy.
	patch(`{"business_name":"BookMeet"}`, 200)
	rec := httptest.NewRecorder()
	h.RequireAuth(h.GetBranding)(rec, authReq(http.MethodGet, "/v1/settings/branding", "", key))
	var b struct {
		PublicTheme string            `json:"public_theme"`
		PublicCopy  map[string]string `json:"public_copy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.PublicTheme != "light" || b.PublicCopy["booking_title"] != "See you <soon> & bring ideas!" || b.PublicCopy["nav_note"] != "A personal hello" || b.PublicCopy["manage_subtitle"] != "" || b.PublicCopy["manage_title"] != "Change of plans?" {
		t.Fatalf("settings lost: %+v", b)
	}
	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	page := httptest.NewRecorder()
	h.BookPage(page, req)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, `name="bookmeet-theme" content="light"`) || !strings.Contains(body, `name="bookmeet-copy"`) || !strings.Contains(body, `bookmeet-appearance`) {
		t.Fatal("native skin/config missing")
	}
	if strings.Contains(body, `See you <soon>`) {
		t.Fatal("copy inserted as HTML")
	}
	if strings.Contains(body, `Choose a time</a>`) || strings.Contains(body, `Manage booking</a>`) {
		t.Fatal("redundant jump action present")
	}
}

func TestBookMeetTimeGroupingSetting(t *testing.T) {
	h, key, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, key)
	for _, body := range []string{`{"group_times":true}`, `{"business_name":"BookMeet"}`, `{"group_times":false}`} {
		rec := httptest.NewRecorder()
		h.RequireAuth(h.PatchBranding)(rec, authReq(http.MethodPatch, "/v1/settings/branding", body, key))
		if rec.Code != 200 {
			t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
		}
		want := !strings.Contains(body, "false")
		rec = httptest.NewRecorder()
		h.RequireAuth(h.GetBranding)(rec, authReq(http.MethodGet, "/v1/settings/branding", "", key))
		var settings map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
		if settings["group_times"] != want {
			t.Fatalf("group_times = %v; want %v", settings["group_times"], want)
		}
		req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
		req.SetPathValue("slug", slug)
		page := httptest.NewRecorder()
		h.BookPage(page, req)
		literal := "false"
		if want {
			literal = "true"
		}
		if page.Code != 200 || !strings.Contains(strings.ReplaceAll(page.Body.String(), " ", ""), "constGROUP_TIMES="+literal+";") {
			t.Fatal("booking page lost grouping preference")
		}
		pub := httptest.NewRecorder()
		h.PublicEventType(pub, req)
		var info map[string]any
		if err := json.Unmarshal(pub.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		if info["group_times"] != want {
			t.Fatal("embed lost grouping preference")
		}
	}
}
