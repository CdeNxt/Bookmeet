package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHomeAddress_roundTripAndKeptWhenOmitted(t *testing.T) {
	h, key, _ := setupWorkspace(t)
	patch := func(body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.RequireAuth(h.PatchMe)(rec, authReq(http.MethodPatch, "/v1/users/me", body, key))
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", body, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := patch(`{"home_address":"  1 Main St, Springfield "}`)["home_address"]; got != "1 Main St, Springfield" {
		t.Fatalf("home_address = %q, want trimmed value", got)
	}
	// An unrelated save (the editor resubmits other fields) must not clear it.
	patch(`{"name":"Renamed"}`)
	rec := httptest.NewRecorder()
	h.RequireAuth(h.GetMe)(rec, authReq(http.MethodGet, "/v1/users/me", "", key))
	var me map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me["home_address"] != "1 Main St, Springfield" {
		t.Fatalf("GET home_address = %v after unrelated patch", me["home_address"])
	}
	// Explicit empty string clears it.
	if got := patch(`{"home_address":""}`)["home_address"]; got != "" {
		t.Fatalf("home_address = %q after clearing", got)
	}
}

func TestHomeAddress_tooLongRejected(t *testing.T) {
	h, key, _ := setupWorkspace(t)
	rec := httptest.NewRecorder()
	h.RequireAuth(h.PatchMe)(rec, authReq(http.MethodPatch, "/v1/users/me", `{"home_address":"`+strings.Repeat("a", 501)+`"}`, key))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
