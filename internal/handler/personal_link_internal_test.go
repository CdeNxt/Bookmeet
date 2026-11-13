package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/personallink"
)

// linkFixture is a workspace with one 30-minute event type, a host free around the clock,
// and nothing in the calendar - so any gap in what a link offers is the link's doing.
type linkFixture struct {
	*inviteFixture
	userID string
	etID   string
	day    time.Time // midnight UTC, three days out
	guests int
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()
	f := &linkFixture{inviteFixture: newInviteFixture(t, false)}
	if rec := f.createEventType(t, "intro", "calendar"); rec.Code != http.StatusCreated {
		t.Fatalf("create event type: %d - %s", rec.Code, rec.Body.String())
	}
	if err := f.h.db.QueryRow(`SELECT id, user_id FROM event_types WHERE slug = 'intro'`).Scan(&f.etID, &f.userID); err != nil {
		t.Fatalf("read event type: %v", err)
	}
	f.day = time.Now().UTC().Add(72 * time.Hour).Truncate(24 * time.Hour)
	return f
}

// at returns hour:minute UTC, `day` days after the fixture's first day.
func (f *linkFixture) at(day, hour, minute int) time.Time {
	return f.day.AddDate(0, 0, day).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

func (f *linkFixture) link(t *testing.T, p personallink.CreateParams) *personallink.Link {
	t.Helper()
	p.UserID, p.EventTypeID = f.userID, f.etID
	if p.GuestName == "" {
		p.GuestName = "Bill Mathers"
	}
	l, _, err := f.h.personalLinks().Create(context.Background(), p)
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	return l
}

// guest returns a fresh attendee, so the per-email booking limits never decide a test.
func (f *linkFixture) guest() booking.Attendee {
	f.guests++
	return booking.Attendee{Name: "Bill Mathers", Email: fmt.Sprintf("guest%d@example.com", f.guests), IANATimezone: "UTC"}
}

func (f *linkFixture) reload(t *testing.T, l *personallink.Link) *personallink.Link {
	t.Helper()
	got, err := f.h.personalLinks().Get(context.Background(), f.userID, l.ID)
	if err != nil {
		t.Fatalf("reload link: %v", err)
	}
	return got
}

func (f *linkFixture) starts(t *testing.T, l *personallink.Link) []string {
	t.Helper()
	res, err := f.h.personalLinkSlots(context.Background(), l, "UTC")
	if err != nil {
		t.Fatalf("personalLinkSlots: %v", err)
	}
	out := make([]string, len(res.Slots))
	for i, s := range res.Slots {
		start, err := time.Parse(time.RFC3339, s.Start)
		if err != nil {
			t.Fatalf("slot start %q: %v", s.Start, err)
		}
		out[i] = start.UTC().Format("01-02 15:04")
	}
	sort.Strings(out)
	return out
}

func stamps(ts ...time.Time) []string {
	out := make([]string, len(ts))
	for i, tm := range ts {
		out[i] = tm.UTC().Format("01-02 15:04")
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The host is free all day every day. The link offers 9:00-10:30 on the first day and
// 14:00-15:00 two days later, and those are the only starts that come back.
func TestPersonalLinkSlots_offerOnlyTheLinksWindowsEachDayItsOwn(t *testing.T) {
	f := newLinkFixture(t)
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{
		{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)},
		{Start: f.at(2, 14, 0), End: f.at(2, 15, 0)},
	}})

	got := f.starts(t, l)
	want := stamps(f.at(0, 9, 0), f.at(0, 9, 30), f.at(0, 10, 0), f.at(2, 14, 0), f.at(2, 14, 30))
	if !sameStrings(got, want) {
		t.Fatalf("offered starts\n got  %v\n want %v", got, want)
	}
}

// The windows narrow what the engine found; they do not decide what is free. A time the
// host is booked for is missing even though the link's window covers it.
func TestPersonalLinkSlots_stillLeaveOutWhatTheHostIsBusyFor(t *testing.T) {
	f := newLinkFixture(t)
	if _, err := f.h.createBookingForSlug(context.Background(), "intro", f.at(0, 9, 30), f.guest(), nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}})

	got := f.starts(t, l)
	want := stamps(f.at(0, 9, 0), f.at(0, 10, 0))
	if !sameStrings(got, want) {
		t.Fatalf("offered starts\n got  %v\n want %v", got, want)
	}
}

func TestPersonalLinkSlots_aLinkThatIsNoLongerUsableOffersNothing(t *testing.T) {
	f := newLinkFixture(t)
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}})
	if err := f.h.personalLinks().Cancel(context.Background(), f.userID, l.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.starts(t, f.reload(t, l)); len(got) != 0 {
		t.Errorf("a cancelled link offers %v", got)
	}
}

func TestBookPersonalLink_booksOnceAndRecordsTheBooking(t *testing.T) {
	f := newLinkFixture(t)
	ctx := context.Background()
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}})

	b, err := f.h.bookPersonalLink(ctx, l, f.at(0, 9, 30), f.guest(), false)
	if err != nil {
		t.Fatalf("first booking: %v", err)
	}
	after := f.reload(t, l)
	if after.Status != personallink.StatusBooked || after.BookingID != b.ID {
		t.Fatalf("link is %q pointing at %q; want booked, %s", after.Status, after.BookingID, b.ID)
	}

	// The same link, a different free time: refused, and nothing second is booked.
	if _, err := f.h.bookPersonalLink(ctx, after, f.at(0, 10, 0), f.guest(), false); !errors.Is(err, personallink.ErrNotBookable) {
		t.Fatalf("second booking through a used link: err = %v; want ErrNotBookable", err)
	}
	var n int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM bookings WHERE status != 'cancelled'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d bookings exist; want exactly the one", n)
	}
}

// Several requests for the same link at once - a double tap, a retry racing the original.
// One books; the rest are turned away before they reach the booking core.
func TestBookPersonalLink_concurrentAttemptsBookExactlyOnce(t *testing.T) {
	f := newLinkFixture(t)
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 12, 0)}}})

	const attempts = 6
	guests := make([]booking.Attendee, attempts)
	for i := range guests {
		guests[i] = f.guest()
	}
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Different free times, so only the link can be what stops the others.
			_, errs[i] = f.h.bookPersonalLink(context.Background(), l, f.at(0, 9, 30*i), guests[i], false)
		}(i)
	}
	wg.Wait()

	booked := 0
	for i, err := range errs {
		switch {
		case err == nil:
			booked++
		case !errors.Is(err, personallink.ErrNotBookable):
			t.Errorf("attempt %d: err = %v; want success or ErrNotBookable", i, err)
		}
	}
	if booked != 1 {
		t.Fatalf("%d attempts booked; want exactly 1", booked)
	}
}

func TestBookPersonalLink_refusesATimeOutsideTheWindows(t *testing.T) {
	f := newLinkFixture(t)
	ctx := context.Background()
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}})

	// 12:00 is free on the host's calendar and not on offer.
	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 12, 0), f.guest(), false); !errors.Is(err, errOutsidePersonalLinkWindows) {
		t.Fatalf("booking outside the windows: err = %v; want errOutsidePersonalLinkWindows", err)
	}
	// 10:15 starts inside the window and runs past its end.
	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 10, 15), f.guest(), false); !errors.Is(err, errOutsidePersonalLinkWindows) {
		t.Fatalf("booking that overruns the window: err = %v; want errOutsidePersonalLinkWindows", err)
	}
	if got := f.reload(t, l).Status; got != personallink.StatusOpen {
		t.Fatalf("link is %q after refused attempts; want it still open", got)
	}

	// The owner approving a time the guest suggested is the one way past the windows.
	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 12, 0), f.guest(), true); err != nil {
		t.Fatalf("owner-approved time outside the windows: %v", err)
	}
	if got := f.reload(t, l).Status; got != personallink.StatusBooked {
		t.Errorf("link is %q; want booked", got)
	}
}

// A booking that cannot be made must not use the link up.
func TestBookPersonalLink_aFailedBookingGivesTheLinkBack(t *testing.T) {
	f := newLinkFixture(t)
	ctx := context.Background()
	if _, err := f.h.createBookingForSlug(ctx, "intro", f.at(0, 9, 30), f.guest(), nil); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	l := f.link(t, personallink.CreateParams{Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}})

	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 9, 30), f.guest(), false); err == nil {
		t.Fatal("booking a time the host is already booked for succeeded")
	}
	if got := f.reload(t, l).Status; got != personallink.StatusOpen {
		t.Fatalf("link is %q after the failed booking; want open", got)
	}
	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 9, 0), f.guest(), false); err != nil {
		t.Fatalf("booking a free time afterwards: %v", err)
	}
}

func TestBookPersonalLink_aReplacedLinkNoLongerBooks(t *testing.T) {
	f := newLinkFixture(t)
	ctx := context.Background()
	window := []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}}
	old := f.link(t, personallink.CreateParams{Windows: window})
	fresh, _, err := f.h.personalLinks().Replace(ctx, f.userID, old.ID, personallink.CreateParams{
		EventTypeID: f.etID, GuestName: old.GuestName, Windows: window,
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	if _, err := f.h.bookPersonalLink(ctx, old, f.at(0, 9, 0), f.guest(), false); !errors.Is(err, personallink.ErrNotBookable) {
		t.Fatalf("booking through the replaced link: err = %v; want ErrNotBookable", err)
	}
	if _, err := f.h.bookPersonalLink(ctx, fresh, f.at(0, 9, 0), f.guest(), false); err != nil {
		t.Fatalf("booking through the new link: %v", err)
	}
}

// A link carrying a place only books on an event type that allows meeting in person; when
// it does not, the attempt fails cleanly and the link is not used up.
func TestBookPersonalLink_aPlaceNeedsAnEventTypeThatAllowsInPerson(t *testing.T) {
	f := newLinkFixture(t)
	ctx := context.Background()
	l := f.link(t, personallink.CreateParams{
		PlaceName: "Equator Coffee", PlaceAddress: "100 Example Street, Mill Valley, CA",
		Windows: []personallink.Window{{Start: f.at(0, 9, 0), End: f.at(0, 10, 30)}},
	})

	if _, err := f.h.bookPersonalLink(ctx, l, f.at(0, 9, 0), f.guest(), false); !errors.Is(err, errInvalidInPersonLocation) {
		t.Fatalf("in person on an event type that does not allow it: err = %v; want errInvalidInPersonLocation", err)
	}
	if got := f.reload(t, l).Status; got != personallink.StatusOpen {
		t.Fatalf("link is %q; want open", got)
	}

	if _, err := f.h.db.Exec(`UPDATE event_types SET allow_in_person = 1 WHERE id = ?`, f.etID); err != nil {
		t.Fatal(err)
	}
	b, err := f.h.bookPersonalLink(ctx, l, f.at(0, 9, 0), f.guest(), false)
	if err != nil {
		t.Fatalf("in person once allowed: %v", err)
	}
	if b.PhysicalLocation != l.PlaceAddress || b.LocationType != "in_person" {
		t.Errorf("booking is %q at %q; want in_person at the link's confirmed address", b.LocationType, b.PhysicalLocation)
	}
}
