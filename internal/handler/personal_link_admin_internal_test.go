package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doID is do for a route with an {id} in its path.
func (f *chatFixture) doID(t *testing.T, fn http.HandlerFunc, method, id string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		fn(w, r)
	}, method, "/v1/personal-links/"+id, "")
}

// The admin list shows the owner's links without their tokens; the address is fetched for
// one link at a time, and only while it still works. Cancelling closes it for the guest.
func TestPersonalLinksAdmin_listCopyAndCancel(t *testing.T) {
	f := newChatFixture(t)

	rec := f.do(t, f.h.ListPersonalLinks, http.MethodGet, "/v1/personal-links", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d - %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), f.token) {
		t.Fatal("the list carries a link's token")
	}
	var out struct {
		Links []personalLinkJSON `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Links) != 1 {
		t.Fatalf("%d links listed; want 1", len(out.Links))
	}
	got := out.Links[0]
	if got.GuestName != "Bill Mathers" || got.EventTypeName != "Intro" || got.Status != "open" || !got.Active || len(got.Windows) != 1 || got.Topic != "the contract" {
		t.Errorf("listed link = %+v", got)
	}

	rec = f.doID(t, f.h.GetPersonalLinkURL, http.MethodGet, f.link.ID)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/i/"+f.token) {
		t.Fatalf("link address: %d - %s", rec.Code, rec.Body.String())
	}

	if rec = f.doID(t, f.h.CancelPersonalLink, http.MethodDelete, f.link.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d - %s", rec.Code, rec.Body.String())
	}
	if l := f.reload(t); l.Status != "cancelled" {
		t.Errorf("link is %q after cancelling", l.Status)
	}
	// Cancelled: no address to copy, nothing left to cancel, and the guest's page is closed.
	if rec = f.doID(t, f.h.GetPersonalLinkURL, http.MethodGet, f.link.ID); rec.Code != http.StatusConflict {
		t.Errorf("address of a cancelled link: %d; want 409", rec.Code)
	}
	if rec = f.doID(t, f.h.CancelPersonalLink, http.MethodDelete, f.link.ID); rec.Code != http.StatusConflict {
		t.Errorf("cancelling twice: %d; want 409", rec.Code)
	}
	if res := f.chat(t, f.token, plinkChatRequest{Action: "open"}); res.Stage != "closed" {
		t.Errorf("guest's page is at %q after cancelling; want closed", res.Stage)
	}
}
