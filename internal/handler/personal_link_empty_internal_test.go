package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/personallink"
	"github.com/calnode/calnode/internal/slots"
)

// fillCalendar makes the owner busy for the whole of the fixture link's window.
func (f *chatFixture) fillCalendar() {
	f.cal.mu.Lock()
	f.cal.busy = []slots.Interval{{Start: f.at(16, 0), End: f.at(19, 0)}}
	f.cal.mu.Unlock()
}

func (f *chatFixture) ownerTexts() []string { return f.tg.sent(ownerChat) }

// A guest who finds nothing to book is not left at a dead end: they are asked which times
// suit them, and the owner hears at once, with a button to offer new times. Told once.
func TestPersonalLinkChat_aLinkWithNoTimesAsksTheGuestAndTellsTheOwnerAtOnce(t *testing.T) {
	f := newChatFixture(t)
	f.fillCalendar()
	before := f.ownerMessages()

	res := f.throughDetails(t)
	last := res.Items[len(res.Items)-1]
	if res.Stage != "times" || !strings.Contains(last.Text, "Which times would suit you?") {
		t.Fatalf("after the details: stage %q, last %q; want the guest asked for their times", res.Stage, last.Text)
	}
	if l := f.reload(t); l.ChatMode != personallink.ChatSuggesting || l.EmptyToldAt.IsZero() {
		t.Errorf("link mode %q, empty told %v; want suggesting and told", l.ChatMode, l.EmptyToldAt)
	}
	told := f.ownerTexts()
	if len(told) != before+1 || !strings.Contains(told[len(told)-1], "has no open times left") ||
		!strings.Contains(told[len(told)-1], "just opened it") {
		t.Fatalf("the owner was told %d messages; last: %v", len(told)-before, told[len(told)-1:])
	}
	if _, ok := f.buttons()["New times for Bill"]; !ok {
		t.Errorf("no New times button; have %v", f.buttons())
	}
	// Reopening the page does not tell the owner again.
	f.chat(t, f.token, plinkChatRequest{Action: "open"})
	f.chat(t, f.token, plinkChatRequest{Action: "times"})
	if n := f.ownerMessages(); n != before+1 {
		t.Errorf("the owner was told %d times; want once", n-before)
	}

	// The guest's suggested times go to the owner now, not when the chat ends, with the
	// buttons to book them and to offer new times.
	other := f.at(20, 0).Add(24 * time.Hour)
	f.chat(t, f.token, plinkChatRequest{Action: "suggest_send", Starts: []string{other.Format(time.RFC3339)}})
	told = f.ownerTexts()
	if len(told) != before+2 || !strings.Contains(told[len(told)-1], "suggested other times") {
		t.Fatalf("after the suggestion the owner got %d messages; last: %v", len(told)-before, told[len(told)-1:])
	}
	b := f.buttons()
	if _, ok := b["New times for Bill"]; !ok {
		t.Errorf("the suggestion message has no New times button; have %v", b)
	}
	if _, ok := b["None of these"]; !ok {
		t.Errorf("the suggestion message lost its own buttons; have %v", b)
	}
	if n := len(f.held(t)); n != 1 {
		t.Errorf("%d items held; want the one set of suggestions", n)
	}
}

// The New times button opens the old offer in the panel to change. It makes nothing itself.
func TestPersonalLinkChat_newTimesReopensTheOffer(t *testing.T) {
	f := newChatFixture(t)
	f.fillCalendar()
	f.throughDetails(t)
	f.tap(t, "New times for Bill")
	if !f.anyText("Here is the old offer") {
		t.Fatalf("the owner was not shown the old offer: %v", f.texts())
	}
	if n := len(f.links(t)); n != 1 {
		t.Errorf("%d links after the tap; want still 1", n)
	}
}

// The sweep notices a link whose times have all gone before a guest does, and says so once.
// A link with times left is left alone.
func TestPersonalLinkSweep_aLinkWhoseTimesRanOutIsToldOnce(t *testing.T) {
	f := newChatFixture(t)
	before := f.ownerMessages()
	f.h.sweepEmptyLinks(context.Background())
	if n := f.ownerMessages(); n != before {
		t.Fatalf("a link with times left drew %d messages", n-before)
	}
	f.fillCalendar()
	f.h.sweepEmptyLinks(context.Background())
	told := f.ownerTexts()
	if len(told) != before+1 || !strings.Contains(told[len(told)-1], "hasn't seen that yet") {
		t.Fatalf("the sweep sent %d messages; last: %v", len(told)-before, told[len(told)-1:])
	}
	f.h.sweepEmptyLinks(context.Background())
	if n := f.ownerMessages(); n != before+1 {
		t.Errorf("a second sweep sent %d more", n-before-1)
	}
	// And the guest who arrives later is not announced a second time.
	f.throughDetails(t)
	if n := f.ownerMessages(); n != before+1 {
		t.Errorf("the guest finding it empty told the owner again (%d)", n-before)
	}
}
