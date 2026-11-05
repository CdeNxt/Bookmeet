package booking_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/calnode/calnode/internal/booking"
	"testing"
	"time"
)

func TestCalendarManageLinkSurvivesRescheduleAndEmailRotation(t *testing.T) {
	db := newTestDB(t)
	svc := booking.New(db)
	ctx := context.Background()
	host := seedHost(t, db)
	et := seedEventType(t, db, host)
	b, err := svc.Create(ctx, booking.CreateParams{EventTypeID: et, HostIDs: []string{host}, StartAt: slot(9, 0), EndAt: slot(9, 30), Organizer: booking.Attendee{Name: "Test", Email: "test@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := svc.IssueCalendarManageToken(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	email, err := svc.IssueManageToken(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(calendar))
	var stored string
	if err = db.QueryRow(`SELECT token_hash FROM booking_manage_tokens WHERE token_hash=?`, "calendar:"+hex.EncodeToString(sum[:])).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == calendar {
		t.Fatal("raw calendar token stored")
	}
	for _, hour := range []int{10, 11} {
		if _, err = svc.Reschedule(ctx, b.ID, slot(hour, 0), slot(hour, 30)); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.RotateManageToken(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		got, err := svc.ValidateManageToken(ctx, calendar)
		if err != nil {
			t.Fatal(err)
		}
		if !got.StartAt.Equal(slot(hour, 0)) {
			t.Fatalf("calendar link has stale time: %v", got.StartAt)
		}
	}
	if _, err = svc.ValidateManageToken(ctx, email); err != booking.ErrTokenNotFound {
		t.Fatalf("old email token valid: %v", err)
	}
	if _, err = svc.CancelByToken(ctx, calendar, "smoke test"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CancelByToken(ctx, calendar, "again"); err != booking.ErrAlreadyCancelled {
		t.Fatalf("cancelled twice: %v", err)
	}
	// Expiry remains enforced for the calendar capability.
	if _, err = db.Exec(`UPDATE booking_manage_tokens SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), stored); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ValidateManageToken(ctx, calendar); err != booking.ErrTokenNotFound {
		t.Fatalf("expired calendar token valid: %v", err)
	}
}
