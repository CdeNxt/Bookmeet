package handler_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBookPage_unknownSlug_returns404(t *testing.T) {
	h, _, _ := setupWorkspace(t)

	req := httptest.NewRequest(http.MethodGet, "/book/no-such-event", nil)
	req.SetPathValue("slug", "no-such-event")
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 for unknown slug", rec.Code)
	}
}

func TestBookPage_knownSlug_returns200WithHTML(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 — body: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Meeting") {
		t.Error("response body missing event type name")
	}
	if !strings.Contains(body, slug) {
		t.Error("response body missing slug (used in JS)")
	}
	if !strings.Contains(body, "30 min") {
		t.Error("response body missing duration label")
	}
}

func TestBookPage_inactiveEventType_returns404(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	// Deactivate the event type.
	patchReq := authReq(http.MethodPatch, "/v1/event-types/"+slug, `{"is_active":false}`, apiKey)
	patchReq.SetPathValue("slug", slug)
	patchRec := httptest.NewRecorder()
	h.RequireAuth(h.PatchEventType)(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch: got %d — %s", patchRec.Code, patchRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404 for inactive event type", rec.Code)
	}
}

func TestBookPage_durationLabels(t *testing.T) {
	cases := []struct {
		mins int
		want string
	}{
		{15, "15 min"},
		{30, "30 min"},
		{45, "45 min"},
		{60, "1 hour"},
		{90, "1 hr 30 min"},
		{120, "2 hours"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(strconv.Itoa(tc.mins)+"min", func(t *testing.T) {
			h, apiKey, _ := setupWorkspace(t)
			slug := fmt.Sprintf("dur-test-%d", tc.mins)
			body := fmt.Sprintf(`{"slug":%q,"name":"Dur Test","duration_minutes":%d}`, slug, tc.mins)
			req := authReq(http.MethodPost, "/v1/event-types", body, apiKey)
			rec := httptest.NewRecorder()
			h.RequireAuth(h.CreateEventType)(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create event type (%d min): %d — %s", tc.mins, rec.Code, rec.Body.String())
			}

			pageReq := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
			pageReq.SetPathValue("slug", slug)
			pageRec := httptest.NewRecorder()
			h.BookPage(pageRec, pageReq)

			if !strings.Contains(pageRec.Body.String(), tc.want) {
				t.Errorf("want %q in page body", tc.want)
			}
		})
	}
}

// An unlisted event type (is_public = 0) is left off the owner's booking page but still
// opens, offers times and takes its form's questions from its own link. It used to answer
// 404, which made "Unlisted (link only)" in the admin a dead link. Switching one off
// entirely is is_active's job, and that still answers 404.
func TestBookPage_unlistedEventType_opensByItsLinkOnly(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	patch := func(body string) {
		t.Helper()
		req := authReq(http.MethodPatch, "/v1/event-types/"+slug, body, apiKey)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		h.RequireAuth(h.PatchEventType)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch %s: got %d - %s", body, rec.Code, rec.Body.String())
		}
	}
	get := func(handler http.HandlerFunc, path string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec.Code
	}

	day := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	patch(`{"is_public":false}`)
	if code := get(h.BookPage, "/book/"+slug); code != http.StatusOK {
		t.Errorf("unlisted booking page = %d; want 200", code)
	}
	if code := get(h.ListQuestions, "/v1/event-types/"+slug+"/questions"); code != http.StatusOK {
		t.Errorf("unlisted questions = %d; want 200", code)
	}
	// The times come from the shared booking core, which has its own gate.
	if code := get(h.GetSlots, "/v1/event-types/"+slug+"/slots?from="+day+"&to="+day+"&tz=UTC"); code != http.StatusOK {
		t.Errorf("unlisted slots = %d; want 200", code)
	}

	patch(`{"is_active":false}`)
	if code := get(h.BookPage, "/book/"+slug); code != http.StatusNotFound {
		t.Errorf("inactive booking page = %d; want 404", code)
	}
	if code := get(h.GetSlots, "/v1/event-types/"+slug+"/slots?from="+day+"&to="+day+"&tz=UTC"); code != http.StatusNotFound {
		t.Errorf("inactive slots = %d; want 404", code)
	}
}

func TestBookPage_locationLabels(t *testing.T) {
	cases := []struct {
		locType  string
		locValue string
		want     string
	}{
		// Online/video/phone types now require a usable value (no calendar connected
		// in this test), so these fixtures supply a valid one.
		{"zoom", "https://zoom.us/j/123456789", "Zoom"},
		{"google_meet", "https://meet.google.com/abc-defg-hij", "Google Meet"},
		{"teams", "https://teams.microsoft.com/l/meetup-join/x", "Microsoft Teams"},
		{"phone", "+1 555 123 4567", "Phone Call"},
		{"in_person", "123 Main St", "123 Main St"},
		{"in_person", "", "In Person"}, // in-person value stays optional
		{"custom_video", "https://example.com/room", "Video Call"},
		{"link", "https://example.com/room", "Video Call"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.locType+"_"+tc.locValue, func(t *testing.T) {
			h, apiKey, _ := setupWorkspace(t)
			slug := "loc-" + tc.locType
			if tc.locValue != "" {
				slug += "-val"
			}
			body := fmt.Sprintf(
				`{"slug":%q,"name":"Loc Test","duration_minutes":30,"location_type":%q,"location_value":%q}`,
				slug, tc.locType, tc.locValue,
			)
			createReq := authReq(http.MethodPost, "/v1/event-types", body, apiKey)
			createRec := httptest.NewRecorder()
			h.RequireAuth(h.CreateEventType)(createRec, createReq)
			if createRec.Code != http.StatusCreated {
				t.Fatalf("create (%s): %d — %s", tc.locType, createRec.Code, createRec.Body.String())
			}

			pageReq := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
			pageReq.SetPathValue("slug", slug)
			pageRec := httptest.NewRecorder()
			h.BookPage(pageRec, pageReq)

			if pageRec.Code != http.StatusOK {
				t.Fatalf("BookPage: %d — %s", pageRec.Code, pageRec.Body.String())
			}
			if !strings.Contains(pageRec.Body.String(), tc.want) {
				t.Errorf("location_type=%q value=%q: want %q in page body", tc.locType, tc.locValue, tc.want)
			}
		})
	}
}

func TestBookPage_rendersIntakeQuestions(t *testing.T) {
	h, _, key, _ := setupWorkspaceWithDB(t)
	slug, _ := seedEventTypeHTTP(t, h, key)

	createQuestion(t, h, slug, key, `{"label":"What are your goals?","type":"text","required":true}`)
	createQuestion(t, h, slug, key, `{"label":"Agree to terms","type":"checkbox"}`)

	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("BookPage: %d — %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "What are your goals?") {
		t.Error("page body missing text question label")
	}
	if !strings.Contains(body, "Agree to terms") {
		t.Error("page body missing checkbox question label")
	}
	if !strings.Contains(body, "required-star") {
		t.Error("page body missing required-star for required field")
	}
}

func TestBookPage_assistantPanelGatedOnLLM(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	render := func() string {
		req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		h.BookPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("BookPage: %d — %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// AI off (default) → no chat panel. Checked via the structural id, not the "Book by
	// chat" text itself — that string now also appears unconditionally on every page as
	// data inside the __CALNODE_I18N JSON blob (internal-docs/i18n-plan.md), so its mere
	// presence no longer implies the assistant UI actually rendered.
	if body := render(); strings.Contains(body, `id="asst-panel"`) {
		t.Error("assistant panel rendered while LLM is disabled")
	}

	// Enable the LLM (dummy endpoint; reload builds a client without a network call).
	prec := httptest.NewRecorder()
	h.RequireAuth(h.PatchLLMSettings)(prec, authReq(http.MethodPatch, "/v1/settings/llm",
		`{"enabled":true,"endpoint":"http://example.test/v1","model":"m"}`, apiKey))
	if prec.Code != http.StatusOK {
		t.Fatalf("enable llm: %d — %s", prec.Code, prec.Body.String())
	}

	// AI on → chat panel + assistant endpoint wired.
	body := render()
	if !strings.Contains(body, `id="asst-panel"`) || !strings.Contains(body, "/assistant") {
		t.Errorf("assistant panel/script missing when LLM enabled")
	}
}

// EU AI Act Art. 50(1): a person must be informed they're interacting with an AI system, at
// the latest at the time of the first interaction. The disclosure must render on first load
// (not appear only after the bot's first reply), sit outside the conversation log (so a
// reset of the chat state — which only clears asst-log's contents client-side — can never
// remove it), and be exposed to assistive tech via its own accessible note, not folded into
// the aria-live conversation region.
func TestBookPage_assistantDisclosure(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	// AI off → no disclosure (nothing to disclose; the panel doesn't exist at all).
	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)
	if strings.Contains(rec.Body.String(), `<p class="asst-disclosure"`) {
		t.Error("disclosure rendered while LLM/assistant is disabled")
	}

	prec := httptest.NewRecorder()
	h.RequireAuth(h.PatchLLMSettings)(prec, authReq(http.MethodPatch, "/v1/settings/llm",
		`{"enabled":true,"endpoint":"http://example.test/v1","model":"m"}`, apiKey))
	if prec.Code != http.StatusOK {
		t.Fatalf("enable llm: %d — %s", prec.Code, prec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req2.SetPathValue("slug", slug)
	rec2 := httptest.NewRecorder()
	h.BookPage(rec2, req2)
	body := rec2.Body.String()

	// html/template escapes the apostrophe (You're → You&#39;re) — correct, safe behavior;
	// a browser renders it identically to the source. Match the escaped form.
	const disclosureText = "You&#39;re chatting with an automated assistant, not a person."
	if !strings.Contains(body, disclosureText) {
		t.Fatal("AI-disclosure notice missing from the rendered chat panel")
	}

	// Present in the accessibility tree as its own announced note, distinct from the
	// aria-live conversation log.
	noteIdx := strings.Index(body, `role="note"`)
	if noteIdx == -1 || !strings.Contains(body[noteIdx:noteIdx+200], disclosureText) {
		t.Error(`disclosure is not exposed via role="note"`)
	}

	// Renders before the conversation log and the user's first input — not appended after
	// the bot's opening reply — and survives a conversation reset because it lives in
	// asst-head, outside asst-log entirely (a client-side reset only ever touches asst-log).
	headIdx := strings.Index(body, `class="asst-head"`)
	logIdx := strings.Index(body, `id="asst-log"`)
	inputIdx := strings.Index(body, `id="asst-input"`)
	discIdx := strings.Index(body, disclosureText)
	if headIdx == -1 || logIdx == -1 || inputIdx == -1 || discIdx == -1 {
		t.Fatal("expected chat panel structure (asst-head/asst-log/asst-input) not found")
	}
	if !(headIdx < discIdx && discIdx < logIdx && logIdx < inputIdx) {
		t.Errorf("disclosure must sit inside asst-head, before asst-log and asst-input; got head=%d disc=%d log=%d input=%d",
			headIdx, discIdx, logIdx, inputIdx)
	}
	if strings.Count(body, disclosureText) != 1 {
		t.Errorf("expected exactly one disclosure instance in asst-head, got %d", strings.Count(body, disclosureText))
	}
}

func TestBookPage_maxFutureDays0(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug := "zero-days"
	body := `{"slug":"zero-days","name":"Zero Days","duration_minutes":30,"max_future_days":0}`
	req := authReq(http.MethodPost, "/v1/event-types", body, apiKey)
	rec := httptest.NewRecorder()
	h.RequireAuth(h.CreateEventType)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create event type: %d — %s", rec.Code, rec.Body.String())
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	pageReq.SetPathValue("slug", slug)
	pageRec := httptest.NewRecorder()
	h.BookPage(pageRec, pageReq)

	if pageRec.Code != http.StatusOK {
		t.Fatalf("BookPage: %d — %s", pageRec.Code, pageRec.Body.String())
	}
	// html/template pads numbers in JS context with spaces, so check via Fields.
	pageBody := pageRec.Body.String()
	maxDaysGot := "(not found)"
	for _, line := range strings.Split(pageBody, "\n") {
		if strings.Contains(line, "MAX_DAYS") {
			fields := strings.Fields(line) // e.g. ["const","MAX_DAYS","=","0",";"]
			if len(fields) >= 4 {
				maxDaysGot = fields[3]
			}
			break
		}
	}
	if maxDaysGot != "0" {
		t.Errorf("want MAX_DAYS = 0 in page body; got MAX_DAYS = %s", maxDaysGot)
	}
}

// The host's "First day of week" (Settings -> Profile) reaches the public booking page.
// The page used to draw a Monday-first calendar whatever the setting said.
func TestBookPage_usesHostWeekStart(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	weekStart := func() string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		h.BookPage(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rec.Code)
		}
		m := regexp.MustCompile(`const WEEK_START\s*=\s*(\d)\s*;`).FindStringSubmatch(rec.Body.String())
		if m == nil {
			t.Fatal("booking page does not set WEEK_START")
		}
		return m[1]
	}

	if got := weekStart(); got != "1" {
		t.Errorf("WEEK_START = %s; want 1 (Monday default)", got)
	}
	if rec := patchMe(t, h, `{"week_start":0}`, apiKey); rec.Code != http.StatusOK {
		t.Fatalf("PATCH /v1/users/me status = %d", rec.Code)
	}
	if got := weekStart(); got != "0" {
		t.Errorf("WEEK_START = %s; want 0 after the host chose Sunday", got)
	}
}

// The skin must be applied before the first paint, or the page shows undressed and the
// page-to-page fade has nothing to animate: the head holds rendering for #bm-ready, and
// the page ends by running the skin and then emitting that marker.
func TestBookPage_skinIsReadyBeforeFirstPaint(t *testing.T) {
	h, apiKey, _ := setupWorkspace(t)
	slug, _ := seedEventTypeHTTP(t, h, apiKey)

	req := httptest.NewRequest(http.MethodGet, "/book/"+slug, nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	h.BookPage(rec, req)
	body := rec.Body.String()

	head, rest, ok := strings.Cut(body, "</head>")
	if !ok {
		t.Fatal("no </head> in the booking page")
	}
	if !strings.Contains(head, `<link rel="expect" href="#bm-ready" blocking="render">`) {
		t.Error("head does not hold the first paint for #bm-ready")
	}
	run := strings.LastIndex(rest, "window.BookMeetSkinInit&&window.BookMeetSkinInit()")
	marker := strings.LastIndex(rest, `<span id="bm-ready" hidden></span>`)
	if run < 0 || marker < run {
		t.Errorf("skin must run before the #bm-ready marker (run at %d, marker at %d)", run, marker)
	}
	if strings.Count(body, `id="bm-ready"`) != 1 {
		t.Errorf("want exactly one #bm-ready marker, got %d", strings.Count(body, `id="bm-ready"`))
	}
	if tail := rest[marker:]; strings.Contains(tail, "<script") {
		t.Error("no script may follow the #bm-ready marker")
	}
}
