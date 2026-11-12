package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/calendar"
)

// readbackProvider is the Google-like inviteProvider plus an event it can be asked to read
// back: the test sets what the host's calendar now shows.
type readbackProvider struct {
	inviteProvider
	rmu   sync.Mutex
	snap  calendar.EventSnapshot
	reads int
}

func (p *readbackProvider) ReadEvent(context.Context, string, string, string) (calendar.EventSnapshot, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	p.reads++
	return p.snap, nil
}

func (p *readbackProvider) show(s calendar.EventSnapshot) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	p.snap = s
}

type readbackFixture struct {
	*inviteFixture
	cal *readbackProvider
	id  string
	was time.Time // the booking's start as Bookmeet wrote it
}

// newReadbackFixture makes a confirmed booking whose calendar event exists, and ages it past
// the quiet period that follows a change made in Bookmeet itself.
func newReadbackFixture(t *testing.T) *readbackFixture {
	t.Helper()
	f := newInviteFixture(t, true)
	cal := &readbackProvider{}
	svc := calendar.NewService(f.h.db)
	svc.Register(cal)
	f.h.SetCalendar(svc)
	if rec := f.createEventType(t, "intro", "calendar"); rec.Code != http.StatusCreated {
		t.Fatalf("create event type: %d - %s", rec.Code, rec.Body.String())
	}
	id, _ := f.book(t, "intro")
	rf := &readbackFixture{inviteFixture: f, cal: cal, id: id}

	deadline := time.Now().Add(3 * time.Second)
	for {
		var eventID string
		_ = f.h.db.QueryRow(`SELECT COALESCE(external_event_id,'') FROM booking_hosts WHERE booking_id = ? AND is_primary = 1`, id).Scan(&eventID)
		if eventID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the booking's calendar event was never recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	rf.age(t)
	b, err := f.h.bookingSvc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	rf.was = b.StartAt
	return rf
}

func (f *readbackFixture) age(t *testing.T) {
	t.Helper()
	if _, err := f.h.db.Exec(`UPDATE bookings SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
}

func (f *readbackFixture) mailCount() int {
	f.mail.mu.Lock()
	defer f.mail.mu.Unlock()
	return len(f.mail.msgs)
}

func (f *readbackFixture) reminderRunAts(t *testing.T) []string {
	t.Helper()
	rows, err := f.h.db.Query(`SELECT run_at FROM jobs WHERE type = 'reminder.send' AND json_extract(payload, '$.booking_id') = ? ORDER BY run_at`, f.id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The reported bug: the host moves the meeting in their calendar. The booking (and so the
// manage link) must show the new time, reminders must move with it, and Bookmeet must not
// email anyone, because the calendar already told the guests.
func TestFollowCalendar_moveInCalendarMovesTheBooking(t *testing.T) {
	f := newReadbackFixture(t)
	ctx := context.Background()
	mails, remindersBefore := f.mailCount(), f.reminderRunAts(t)

	// Outside any availability window, on purpose: a manual move is accepted as made.
	moved := f.was.Add(26*time.Hour + 15*time.Minute)
	f.cal.show(calendar.EventSnapshot{State: calendar.EventPresent, Start: moved, End: moved.Add(45 * time.Minute)})

	if !f.h.followCalendar(ctx, f.id) {
		t.Fatal("followCalendar reported no change for a moved event")
	}
	b, err := f.h.bookingSvc.Get(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if !b.StartAt.Equal(moved) || !b.EndAt.Equal(moved.Add(45*time.Minute)) || b.Status != "confirmed" {
		t.Fatalf("booking = %v - %v (%s); want the calendar's %v + 45m, confirmed", b.StartAt, b.EndAt, b.Status, moved)
	}
	after := f.reminderRunAts(t)
	if len(after) == 0 || len(remindersBefore) == 0 || after[0] == remindersBefore[0] {
		t.Errorf("reminders were not re-timed: before %v, after %v", remindersBefore, after)
	}
	time.Sleep(50 * time.Millisecond) // any stray background send would land by now
	if got := f.mailCount(); got != mails {
		t.Errorf("Bookmeet sent %d email(s) for a move made in the calendar; want none", got-mails)
	}

	// In step now: a second look changes nothing.
	f.age(t)
	if f.h.followCalendar(ctx, f.id) {
		t.Error("followCalendar changed a booking that already matches the calendar")
	}
}

func TestFollowCalendar_deletionInCalendarCancelsTheBooking(t *testing.T) {
	f := newReadbackFixture(t)
	ctx := context.Background()
	mails := f.mailCount()
	f.cal.show(calendar.EventSnapshot{State: calendar.EventCancelled})

	if !f.h.followCalendar(ctx, f.id) {
		t.Fatal("followCalendar reported no change for a deleted event")
	}
	b, err := f.h.bookingSvc.Get(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "cancelled" || b.CancellationReason != cancelledInCalendarReason {
		t.Fatalf("booking = %s (%q); want cancelled in the calendar", b.Status, b.CancellationReason)
	}
	var eventID string
	_ = f.h.db.QueryRow(`SELECT COALESCE(external_event_id,'') FROM booking_hosts WHERE booking_id = ? AND is_primary = 1`, f.id).Scan(&eventID)
	if eventID != "" {
		t.Errorf("event id %q kept: the reconciler would try to delete an event that is already gone", eventID)
	}
	if left := f.reminderRunAts(t); len(left) != 0 {
		t.Errorf("reminders still queued for a cancelled booking: %v", left)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.mailCount(); got != mails {
		t.Errorf("Bookmeet sent %d email(s) for a deletion made in the calendar; want none", got-mails)
	}
}

// Everything that must NOT change the booking.
func TestFollowCalendar_leavesTheBookingAlone(t *testing.T) {
	later := func(f *readbackFixture) calendar.EventSnapshot {
		return calendar.EventSnapshot{State: calendar.EventPresent, Start: f.was.Add(time.Hour), End: f.was.Add(90 * time.Minute)}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *readbackFixture)
	}{
		{"the calendar cannot say", func(t *testing.T, f *readbackFixture) { f.cal.show(calendar.EventSnapshot{}) }},
		{"a bare not-found reaches the booking as unknown", func(t *testing.T, f *readbackFixture) {
			f.cal.show(calendar.EventSnapshot{State: calendar.EventMissing})
		}},
		{"Bookmeet changed the booking moments ago", func(t *testing.T, f *readbackFixture) {
			f.cal.show(later(f))
			if _, err := f.h.db.Exec(`UPDATE bookings SET updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), f.id); err != nil {
				t.Fatal(err)
			}
		}},
		{"Bookmeet's own move has not reached the calendar yet", func(t *testing.T, f *readbackFixture) {
			f.cal.show(later(f))
			if _, err := f.h.db.Exec(`UPDATE booking_hosts SET needs_sync = 1 WHERE booking_id = ?`, f.id); err != nil {
				t.Fatal(err)
			}
		}},
		{"the booking is already cancelled", func(t *testing.T, f *readbackFixture) {
			f.cal.show(later(f))
			if err := f.h.bookingSvc.CancelByID(context.Background(), f.id, "by the guest"); err != nil {
				t.Fatal(err)
			}
			f.age(t)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadbackFixture(t)
			tc.setup(t, f)
			if f.h.followCalendar(context.Background(), f.id) {
				t.Fatal("followCalendar changed the booking")
			}
			b, err := f.h.bookingSvc.Get(context.Background(), f.id)
			if err != nil {
				t.Fatal(err)
			}
			if !b.StartAt.Equal(f.was) {
				t.Errorf("start moved to %v; want it left at %v", b.StartAt, f.was)
			}
		})
	}
}

// "No such event" means deleted only when the account answering is the one that wrote the
// event. A host who swaps Google accounts must not have every booking read as deleted.
func TestReadEvent_missingIsTrustedOnlyFromTheWritingAccount(t *testing.T) {
	f := newReadbackFixture(t)
	ctx := context.Background()
	if _, err := f.h.db.Exec(`UPDATE calendar_connections SET account_email = 'now@host.example'`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := f.h.db.QueryRow(`SELECT user_id FROM booking_hosts WHERE booking_id = ?`, f.id).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	f.cal.show(calendar.EventSnapshot{State: calendar.EventMissing})
	for account, want := range map[string]calendar.EventState{
		"now@host.example":    calendar.EventCancelled,
		"before@host.example": calendar.EventUnknown,
		"":                    calendar.EventUnknown,
	} {
		snap, err := f.h.getCal().ReadEvent(ctx, userID, "primary", "evt-1", "google", account)
		if err != nil || snap.State != want {
			t.Errorf("event written by %q: state %v err %v; want %v", account, snap.State, err, want)
		}
	}
}

// A new booking records which account wrote its event, so the rule above has something to
// compare against.
func TestCreateBooking_stampsTheEventAccount(t *testing.T) {
	f := newInviteFixture(t, true)
	if _, err := f.h.db.Exec(`UPDATE calendar_connections SET account_email = 'jeroen@gmail.example'`); err != nil {
		t.Fatal(err)
	}
	f.createEventType(t, "intro", "calendar")
	id, _ := f.book(t, "intro")
	deadline := time.Now().Add(3 * time.Second)
	for {
		var account string
		_ = f.h.db.QueryRow(`SELECT COALESCE(external_account,'') FROM booking_hosts WHERE booking_id = ?`, id).Scan(&account)
		if account == "jeroen@gmail.example" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("external_account = %q; want the destination account", account)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Opening the manage link shows the booking as the calendar has it now.
func TestManagePage_followsTheCalendarBeforeRendering(t *testing.T) {
	f := newReadbackFixture(t)
	ctx := context.Background()
	token, err := f.h.bookingSvc.IssueManageToken(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	f.age(t)
	moved := f.was.Add(48 * time.Hour)
	f.cal.show(calendar.EventSnapshot{State: calendar.EventPresent, Start: moved, End: moved.Add(30 * time.Minute)})

	req := httptest.NewRequest(http.MethodGet, "/manage/"+token, nil)
	req.SetPathValue("token", token)
	rec := httptest.NewRecorder()
	f.h.ManagePage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manage page: %d", rec.Code)
	}
	b, err := f.h.bookingSvc.Get(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	if !b.StartAt.Equal(moved) {
		t.Fatalf("after opening the manage link the booking starts %v; want %v", b.StartAt, moved)
	}
}

// The sweep covers upcoming bookings nobody opens, and paces itself.
func TestFollowCalendarSweep_followsUpcomingBookingsAndPacesItself(t *testing.T) {
	f := newReadbackFixture(t)
	ctx := context.Background()
	moved := f.was.Add(3 * time.Hour)
	f.cal.show(calendar.EventSnapshot{State: calendar.EventPresent, Start: moved, End: moved.Add(30 * time.Minute)})

	f.h.followCalendarSweep(ctx)
	b, _ := f.h.bookingSvc.Get(ctx, f.id)
	if !b.StartAt.Equal(moved) {
		t.Fatalf("sweep left the booking at %v; want %v", b.StartAt, moved)
	}
	f.cal.rmu.Lock()
	reads := f.cal.reads
	f.cal.rmu.Unlock()
	f.h.followCalendarSweep(ctx)
	f.cal.rmu.Lock()
	defer f.cal.rmu.Unlock()
	if f.cal.reads != reads {
		t.Errorf("a second sweep straight away read the calendar again (%d -> %d)", reads, f.cal.reads)
	}
}
