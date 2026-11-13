package personallink_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/calnode/calnode/internal/db"
	"github.com/calnode/calnode/internal/personallink"
)

const (
	owner = "user-owner"
	other = "user-other"
	etOwn = "et-own"
)

// monday is the first day of the week the tests offer; "now" starts five days before it.
var monday = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

type env struct {
	svc *personallink.Service
	db  *sql.DB
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	database, err := db.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	for _, q := range []string{
		`INSERT INTO users (id, email, name) VALUES ('` + owner + `', 'owner@example.com', 'Owner')`,
		`INSERT INTO users (id, email, name) VALUES ('` + other + `', 'other@example.com', 'Other')`,
		`INSERT INTO event_types (id, user_id, slug, name, duration_minutes) VALUES ('` + etOwn + `', '` + owner + `', 'intro', 'Intro', 30)`,
		`INSERT INTO event_types (id, user_id, slug, name, duration_minutes) VALUES ('et-other', '` + other + `', 'theirs', 'Theirs', 30)`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	e := &env{db: database, now: monday.AddDate(0, 0, -5)}
	e.svc = personallink.New(database, [32]byte{1})
	e.svc.SetClock(func() time.Time { return e.now })
	return e
}

// at returns hour:00 UTC on the given day of the offered week (0 = Monday).
func at(day, hour int) time.Time {
	return monday.AddDate(0, 0, day).Add(time.Duration(hour) * time.Hour)
}

func win(day, from, to int) personallink.Window {
	return personallink.Window{Start: at(day, from), End: at(day, to)}
}

func (e *env) create(t *testing.T, name string, windows ...personallink.Window) (*personallink.Link, string) {
	t.Helper()
	l, token, err := e.svc.Create(context.Background(), personallink.CreateParams{
		UserID: owner, EventTypeID: etOwn, GuestName: name, Windows: windows,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return l, token
}

func (e *env) status(t *testing.T, id string) string {
	t.Helper()
	l, err := e.svc.Get(context.Background(), owner, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return l.Status
}

func TestCreate_keepsAWindowPerDayAndMergesOnesThatTouch(t *testing.T) {
	e := newEnv(t)
	// Tuesday 9-11 and 11-12 are one window said in two pieces; Thursday 13-16 is its own.
	l, _ := e.create(t, "Bill Mathers", win(3, 13, 16), win(1, 11, 12), win(1, 9, 11))

	got, err := e.svc.Get(context.Background(), owner, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []personallink.Window{win(1, 9, 12), win(3, 13, 16)}
	if len(got.Windows) != len(want) {
		t.Fatalf("windows = %v; want %v", got.Windows, want)
	}
	for i := range want {
		if !got.Windows[i].Start.Equal(want[i].Start) || !got.Windows[i].End.Equal(want[i].End) {
			t.Errorf("window %d = %v; want %v", i, got.Windows[i], want[i])
		}
	}
	if !got.ExpiresAt.Equal(at(3, 16)) {
		t.Errorf("expires %v; want the end of the last window, %v", got.ExpiresAt, at(3, 16))
	}
	if got.Status != personallink.StatusOpen {
		t.Errorf("status = %q; want open", got.Status)
	}
}

func TestCreate_rejectsWhatCannotBeOffered(t *testing.T) {
	e := newEnv(t)
	good := []personallink.Window{win(0, 9, 12)}
	cases := map[string]personallink.CreateParams{
		"no guest name":         {UserID: owner, EventTypeID: etOwn, Windows: good},
		"no windows":            {UserID: owner, EventTypeID: etOwn, GuestName: "Bill"},
		"window ends first":     {UserID: owner, EventTypeID: etOwn, GuestName: "Bill", Windows: []personallink.Window{{Start: at(0, 12), End: at(0, 9)}}},
		"every window is past":  {UserID: owner, EventTypeID: etOwn, GuestName: "Bill", Windows: []personallink.Window{win(-30, 9, 12)}},
		"someone else's event":  {UserID: owner, EventTypeID: "et-other", GuestName: "Bill", Windows: good},
		"place with no address": {UserID: owner, EventTypeID: etOwn, GuestName: "Bill", PlaceName: "Equator Coffee", Windows: good},
	}
	for name, p := range cases {
		if _, _, err := e.svc.Create(context.Background(), p); !errors.Is(err, personallink.ErrInvalid) {
			t.Errorf("%s: err = %v; want ErrInvalid", name, err)
		}
	}
}

func TestToken_findsTheLinkAndCanBeShownAgain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, token := e.create(t, "Bill Mathers", win(0, 9, 12))

	got, err := e.svc.ByToken(ctx, token)
	if err != nil || got.ID != l.ID {
		t.Fatalf("ByToken = %v, %v; want link %s", got, err, l.ID)
	}
	if _, err := e.svc.ByToken(ctx, token+"x"); !errors.Is(err, personallink.ErrNotFound) {
		t.Errorf("wrong token: err = %v; want ErrNotFound", err)
	}
	if _, err := e.svc.ByToken(ctx, ""); !errors.Is(err, personallink.ErrNotFound) {
		t.Errorf("empty token: err = %v; want ErrNotFound", err)
	}

	// The lost-link case: the owner asks for the same URL again.
	again, err := e.svc.Token(ctx, owner, l.ID)
	if err != nil || again != token {
		t.Errorf("Token = %q, %v; want the original token", again, err)
	}
	if _, err := e.svc.Token(ctx, other, l.ID); !errors.Is(err, personallink.ErrNotFound) {
		t.Errorf("another user reading the token: err = %v; want ErrNotFound", err)
	}

	// The token itself is never at rest in the clear.
	var hash, enc string
	if err := e.db.QueryRow(`SELECT token_hash, token_enc FROM personal_links WHERE id = ?`, l.ID).Scan(&hash, &enc); err != nil {
		t.Fatal(err)
	}
	if hash == token || enc == token {
		t.Error("the token is stored in the clear")
	}
}

func TestExpiry_appliesOnReadButNotWhileSuggestionsWait(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	open, _ := e.create(t, "Bill Mathers", win(0, 9, 12))
	waiting, _ := e.create(t, "Kendall Roy", win(0, 9, 12))
	if err := e.svc.SetStatus(ctx, waiting.ID, personallink.StatusProposed); err != nil {
		t.Fatal(err)
	}

	e.now = at(0, 13) // an hour after the only window closed
	if got := e.status(t, open.ID); got != personallink.StatusExpired {
		t.Errorf("open link after its last window: %q; want expired", got)
	}
	// Suggested times can fall after the offered windows, so the owner's answer must
	// still be able to book.
	if got := e.status(t, waiting.ID); got != personallink.StatusProposed {
		t.Errorf("link waiting on the owner: %q; want it left as proposed", got)
	}
	if err := e.svc.Claim(ctx, waiting.ID); err != nil {
		t.Errorf("claiming a link with suggestions pending, after its windows: %v", err)
	}
	if err := e.svc.Claim(ctx, open.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Errorf("claiming an expired link: err = %v; want ErrNotBookable", err)
	}
}

func TestClaim_isSingleUse(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, _ := e.create(t, "Bill Mathers", win(0, 9, 12))
	if _, err := e.db.Exec(`INSERT INTO bookings (id, event_type_id, host_id, start_at, end_at, status)
		VALUES ('bk-1', ?, ?, ?, ?, 'confirmed')`, etOwn, owner, at(0, 9).Format(time.RFC3339), at(0, 9).Add(30*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Claim(ctx, l.ID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := e.svc.Claim(ctx, l.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Fatalf("second claim while the first is running: err = %v; want ErrNotBookable", err)
	}

	// A booking attempt that fails gives the link back as it was.
	if err := e.svc.Release(ctx, l.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := e.status(t, l.ID); got != personallink.StatusOpen {
		t.Fatalf("after release: %q; want open", got)
	}

	if err := e.svc.Claim(ctx, l.ID); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if err := e.svc.MarkBooked(ctx, l.ID, "bk-1"); err != nil {
		t.Fatalf("mark booked: %v", err)
	}
	got, _ := e.svc.Get(ctx, owner, l.ID)
	if got.Status != personallink.StatusBooked || got.BookingID != "bk-1" {
		t.Errorf("after booking: status %q booking %q; want booked, bk-1", got.Status, got.BookingID)
	}
	if err := e.svc.Claim(ctx, l.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Errorf("claiming a used link: err = %v; want ErrNotBookable", err)
	}
}

func TestClaim_aDeadAttemptDoesNotLockTheLinkForever(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, _ := e.create(t, "Bill Mathers", win(0, 9, 12))
	if err := e.svc.SetStatus(ctx, l.ID, personallink.StatusFollowUp); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Claim(ctx, l.ID); err != nil { // an attempt that never finishes
		t.Fatal(err)
	}

	e.now = e.now.Add(time.Minute)
	if err := e.svc.Claim(ctx, l.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Fatalf("a minute later: err = %v; want the first claim still held", err)
	}
	e.now = e.now.Add(2 * time.Minute)
	if err := e.svc.Claim(ctx, l.ID); err != nil {
		t.Fatalf("after the stale-claim timeout: %v", err)
	}
	// The status the link held before the dead attempt survives the takeover.
	if err := e.svc.Release(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.status(t, l.ID); got != personallink.StatusFollowUp {
		t.Errorf("released to %q; want followup", got)
	}
}

func TestCancel_stopsTheLinkAndOnlyTheOwnerCan(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, _ := e.create(t, "Bill Mathers", win(0, 9, 12))

	if err := e.svc.Cancel(ctx, other, l.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Fatalf("another user cancelling: err = %v; want it refused", err)
	}
	if err := e.svc.Cancel(ctx, owner, l.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := e.svc.Claim(ctx, l.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Errorf("claiming a cancelled link: err = %v; want ErrNotBookable", err)
	}
}

func TestReplace_retiresTheOldLinkAndCarriesWhatTheGuestGave(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old, oldToken := e.create(t, "Bill Mathers", win(0, 9, 12))
	if err := e.svc.SaveGuestDetails(ctx, old.ID, "bill@example.com", "555 010 0142"); err != nil {
		t.Fatal(err)
	}

	fresh, freshToken, err := e.svc.Replace(ctx, owner, old.ID, personallink.CreateParams{
		EventTypeID: etOwn, GuestName: "bill mathers", PlaceName: "Equator Coffee", PlaceAddress: "100 Example Street",
		Windows: []personallink.Window{win(2, 13, 16)},
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if fresh.GuestEmail != "bill@example.com" || fresh.GuestPhone != "555 010 0142" {
		t.Errorf("new link has email %q phone %q; want the guest's details carried over", fresh.GuestEmail, fresh.GuestPhone)
	}
	if fresh.ReplacesID != old.ID || freshToken == oldToken {
		t.Errorf("new link replaces %q with token reuse %v; want it to point at the old link with its own token", fresh.ReplacesID, freshToken == oldToken)
	}

	stale, err := e.svc.ByToken(ctx, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Status != personallink.StatusReplaced || stale.Active() {
		t.Errorf("old link is %q; want replaced and no longer usable", stale.Status)
	}
	if err := e.svc.Claim(ctx, old.ID); !errors.Is(err, personallink.ErrNotBookable) {
		t.Errorf("booking through the old link: err = %v; want ErrNotBookable", err)
	}

	// A link for someone else starts clean.
	next, _, err := e.svc.Replace(ctx, owner, fresh.ID, personallink.CreateParams{
		EventTypeID: etOwn, GuestName: "Kendall Roy", Windows: []personallink.Window{win(2, 13, 16)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.GuestEmail != "" {
		t.Errorf("a different guest inherited %q", next.GuestEmail)
	}
}

func TestReplace_refusesALinkThatWasUsed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	l, _ := e.create(t, "Bill Mathers", win(0, 9, 12))
	if err := e.svc.Claim(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.svc.Replace(ctx, owner, l.ID, personallink.CreateParams{
		EventTypeID: etOwn, GuestName: "Bill Mathers", Windows: []personallink.Window{win(2, 13, 16)},
	})
	if !errors.Is(err, personallink.ErrNotBookable) {
		t.Errorf("replacing a link mid-booking: err = %v; want ErrNotBookable", err)
	}
}

func TestCovers_needsTheWholeMeetingInsideOneWindow(t *testing.T) {
	l := &personallink.Link{Windows: []personallink.Window{win(1, 9, 12), win(3, 13, 16)}}
	half := 30 * time.Minute
	cases := []struct {
		name  string
		start time.Time
		want  bool
	}{
		{"first start of a window", at(1, 9), true},
		{"last start that still fits", at(1, 12).Add(-half), true},
		{"runs past the end", at(1, 12).Add(-half / 2), false},
		{"starts before the window", at(1, 9).Add(-half / 2), false},
		{"another day's hours on this day", at(1, 13), false},
		{"the other day's window", at(3, 13), true},
		{"a day not offered", at(2, 10), false},
	}
	for _, c := range cases {
		if got := l.Covers(c.start, c.start.Add(half)); got != c.want {
			t.Errorf("%s: Covers = %v; want %v", c.name, got, c.want)
		}
	}
}

func TestList_isTheOwnersLogNewestFirst(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	first, _ := e.create(t, "Bill Mathers", win(0, 9, 12))
	e.now = e.now.Add(time.Hour)
	second, _ := e.create(t, "Kendall Roy", win(1, 9, 12))

	got, err := e.svc.List(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("List = %d links; want the two, newest first", len(got))
	}
	if theirs, _ := e.svc.List(ctx, other); len(theirs) != 0 {
		t.Errorf("another user sees %d of the owner's links", len(theirs))
	}
}
