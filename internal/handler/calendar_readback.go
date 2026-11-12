package handler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/calnode/calnode/internal/booking"
	"github.com/calnode/calnode/internal/calendar"
	"github.com/calnode/calnode/internal/webhook"
)

// Following the host's calendar.
//
// Bookmeet writes each booking to the host's calendar and, until this existed, never looked
// at it again. A host who dragged the event to another time, or deleted it, left the booking
// (and its manage link, reminders and the slot it held) at the original time. Here the
// primary host's event is read back and the booking is made to match:
//
//   - moved in the calendar  -> the booking takes the event's start and end. No availability
//     or overlap check: the host did it on purpose and the calendar already shows it.
//   - cancelled or deleted   -> the booking is cancelled.
//
// Bookmeet sends no email for either. The calendar the host edited tells the guests, and a
// second notice from Bookmeet would only repeat it. Webhooks do fire, and reminders are
// re-timed or dropped, because those describe the booking, whoever changed it.
//
// Only time and cancellation are followed. A changed title, location or guest list is not.
//
// It runs when a manage link is opened (so that page is never stale) and on a sweep of
// upcoming bookings, for every provider that implements calendar.EventReader.

const (
	// calReadbackInterval is how often the sweep reads upcoming events back. Each booking
	// costs one provider request, so this is deliberately slower than the reconciler tick.
	calReadbackInterval = 10 * time.Minute
	// calReadbackQuiet leaves a booking alone for this long after Bookmeet itself changed
	// it. A reschedule made in Bookmeet updates the booking first and the calendar a moment
	// later; reading the event in between would see the old time and undo the reschedule.
	calReadbackQuiet = 2 * time.Minute
	// calReadbackHorizon and calReadbackBatch bound one sweep.
	calReadbackHorizon = 90 * 24 * time.Hour
	calReadbackBatch   = 300
	// calReadbackPageWait is the longest a manage page waits on the calendar.
	calReadbackPageWait = 4 * time.Second

	cancelledInCalendarReason = "Cancelled in the host's calendar"
)

// calReadbackInFlight holds the ids of bookings being followed right now, so a manage page
// and the sweep never apply the same change twice.
var calReadbackInFlight sync.Map

// followCalendar reads one booking's calendar event back and applies a move or cancellation
// to the booking. It reports whether the booking changed. Every "cannot tell" case (no
// calendar, no event recorded, a provider error, an event the account cannot be shown to
// hold) changes nothing.
func (h *Handler) followCalendar(ctx context.Context, bookingID string) bool {
	gc := h.getCal()
	if gc == nil {
		return false
	}
	if _, busy := calReadbackInFlight.LoadOrStore(bookingID, struct{}{}); busy {
		return false
	}
	defer calReadbackInFlight.Delete(bookingID)

	var etID, startStr, endStr, updatedStr, userID, eventID, calendarID, provider, account string
	err := h.db.QueryRowContext(ctx, `
		SELECT b.event_type_id, b.start_at, b.end_at, b.updated_at, bh.user_id, bh.external_event_id,
		       COALESCE(bh.external_calendar_id, ''), COALESCE(bh.external_provider, ''), COALESCE(bh.external_account, '')
		FROM bookings b JOIN booking_hosts bh ON bh.booking_id = b.id AND bh.is_primary = 1
		WHERE b.id = ? AND b.status = 'confirmed' AND b.end_at > ?
		  AND COALESCE(bh.external_event_id, '') != '' AND bh.needs_sync = 0`,
		bookingID, time.Now().UTC().Format(time.RFC3339)).
		Scan(&etID, &startStr, &endStr, &updatedStr, &userID, &eventID, &calendarID, &provider, &account)
	if err != nil {
		return false // not an upcoming confirmed booking with an event in step: nothing to follow
	}
	start, err1 := time.Parse(time.RFC3339Nano, startStr)
	end, err2 := time.Parse(time.RFC3339Nano, endStr)
	updated, err3 := time.Parse(time.RFC3339Nano, updatedStr)
	if err1 != nil || err2 != nil || err3 != nil || time.Since(updated) < calReadbackQuiet {
		return false
	}

	snap, err := gc.ReadEvent(ctx, userID, calendarID, eventID, provider, account)
	if err != nil {
		h.logger.Debug("calendar read-back: read event", "error", err, "booking_id", bookingID)
		return false
	}
	switch snap.State {
	case calendar.EventPresent:
		if snap.Start.Truncate(time.Second).Equal(start.Truncate(time.Second)) &&
			snap.End.Truncate(time.Second).Equal(end.Truncate(time.Second)) {
			return false
		}
		return h.followCalendarMove(bookingID, etID, userID, start, end, snap.Start, snap.End)
	case calendar.EventCancelled:
		return h.followCalendarCancel(bookingID, userID)
	}
	return false
}

// followCalendarMove sets the booking to the event's new time and brings everything that
// hangs off the time along: other hosts' events, the Zoom meeting, LiveKit links, reminders,
// and the booking.rescheduled webhook. No email, and the manage link is not rotated: the
// guest may be opening it this very moment.
func (h *Handler) followCalendarMove(bookingID, etID, primaryUserID string, prevStart, prevEnd, newStart, newEnd time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b, err := h.bookingSvc.FollowCalendarTime(ctx, bookingID, newStart, newEnd)
	if err != nil {
		if errors.Is(err, booking.ErrDoubleBooked) {
			h.logger.Warn("calendar read-back: event moved onto another booking's exact start; booking left as it was",
				"booking_id", bookingID, "event_start", newStart)
		} else if !errors.Is(err, booking.ErrAlreadyCancelled) && !errors.Is(err, booking.ErrNotFound) {
			h.logger.Error("calendar read-back: follow move", "error", err, "booking_id", bookingID)
		}
		return false
	}
	h.logger.Info("calendar read-back: booking follows a move made in the calendar",
		"booking_id", bookingID, "from", prevStart, "to", newStart)

	// The primary host's event is already right. Any other host's copy is now at the old
	// time: flag it and let the reconciler move it.
	if res, err := h.db.ExecContext(ctx, `
		UPDATE booking_hosts SET needs_sync = 1
		WHERE booking_id = ? AND user_id != ? AND COALESCE(external_event_id, '') != ''`,
		bookingID, primaryUserID); err != nil {
		h.logger.Error("calendar read-back: flag other hosts' events", "error", err, "booking_id", bookingID)
	} else if n, _ := res.RowsAffected(); n > 0 {
		h.nudgeCalendarReconcile()
	}

	h.remintLiveKitLinks(ctx, b)
	h.rescheduleZoomMeeting(ctx, b)
	if err := h.replaceReminderJobs(ctx, b.ID, etID, b.StartAt); err != nil {
		h.logger.Error("calendar read-back: replace reminder jobs", "error", err, "booking_id", b.ID)
	}
	if h.webhookSvc != nil {
		if err := h.webhookSvc.Enqueue(ctx, "booking.rescheduled", webhook.BookingPayload{
			ID:              b.ID,
			EventTypeSlug:   h.eventTypeSlug(ctx, etID),
			HostID:          b.HostID,
			StartAt:         b.StartAt.UTC().Format(time.RFC3339),
			EndAt:           b.EndAt.UTC().Format(time.RFC3339),
			Status:          b.Status,
			LocationValue:   b.LocationValue,
			CreatedAt:       b.CreatedAt.UTC().Format(time.RFC3339),
			PreviousStartAt: prevStart.UTC().Format(time.RFC3339),
			PreviousEndAt:   prevEnd.UTC().Format(time.RFC3339),
		}); err != nil {
			h.logger.Error("calendar read-back: enqueue booking.rescheduled webhook", "error", err, "booking_id", b.ID)
		}
	}
	return true
}

// followCalendarCancel cancels a booking whose event the host cancelled or deleted. The
// event is already gone, so its id is cleared rather than deleted again; other hosts' copies
// are removed by the reconciler. No email. A paid booking is NOT refunded here: moving
// money on an inferred signal is left to the host, who can refund from the admin.
func (h *Handler) followCalendarCancel(bookingID, primaryUserID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.bookingSvc.CancelByID(ctx, bookingID, cancelledInCalendarReason); err != nil {
		if !errors.Is(err, booking.ErrAlreadyCancelled) && !errors.Is(err, booking.ErrNotFound) {
			h.logger.Error("calendar read-back: follow cancellation", "error", err, "booking_id", bookingID)
		}
		return false
	}
	h.logger.Info("calendar read-back: booking cancelled because its calendar event was removed", "booking_id", bookingID)

	if _, err := h.db.ExecContext(ctx,
		`UPDATE booking_hosts SET external_event_id = NULL WHERE booking_id = ? AND user_id = ?`,
		bookingID, primaryUserID); err != nil {
		h.logger.Error("calendar read-back: clear removed event id", "error", err, "booking_id", bookingID)
	}
	if _, err := h.db.ExecContext(ctx, `UPDATE bookings SET external_event_id = NULL WHERE id = ?`, bookingID); err != nil {
		h.logger.Error("calendar read-back: clear removed booking event id", "error", err, "booking_id", bookingID)
	}
	h.nudgeCalendarReconcile() // removes any other host's copy of the event

	if _, err := h.db.ExecContext(ctx, `
		DELETE FROM jobs
		WHERE type = 'reminder.send' AND json_extract(payload, '$.booking_id') = ? AND status != 'running'`,
		bookingID); err != nil {
		h.logger.Error("calendar read-back: drop reminder jobs", "error", err, "booking_id", bookingID)
	}

	b, err := h.bookingSvc.Get(ctx, bookingID)
	if err != nil {
		h.logger.Error("calendar read-back: reload cancelled booking", "error", err, "booking_id", bookingID)
		return true
	}
	h.cancelZoomMeeting(ctx, b)
	if h.webhookSvc != nil {
		if err := h.webhookSvc.Enqueue(ctx, "booking.cancelled", webhook.BookingPayload{
			ID:                 b.ID,
			EventTypeSlug:      h.eventTypeSlug(ctx, b.EventTypeID),
			HostID:             b.HostID,
			StartAt:            b.StartAt.UTC().Format(time.RFC3339),
			EndAt:              b.EndAt.UTC().Format(time.RFC3339),
			Status:             b.Status,
			CancellationReason: b.CancellationReason,
			LocationValue:      b.LocationValue,
			CreatedAt:          b.CreatedAt.UTC().Format(time.RFC3339),
			PaymentStatus:      paymentStatusForWebhook(b.PaymentStatus),
			AmountPaidCents:    b.AmountPaidCents,
			AmountPaidCurrency: b.AmountPaidCurrency,
		}); err != nil {
			h.logger.Error("calendar read-back: enqueue booking.cancelled webhook", "error", err, "booking_id", b.ID)
		}
	}
	return true
}

func (h *Handler) eventTypeSlug(ctx context.Context, etID string) string {
	var slug string
	_ = h.db.QueryRowContext(ctx, `SELECT slug FROM event_types WHERE id = ?`, etID).Scan(&slug)
	return slug
}

// followCalendarOnPage is followCalendar for a page that is about to show the booking: it
// waits only briefly on the calendar, and returns the booking as it now stands.
func (h *Handler) followCalendarOnPage(ctx context.Context, b *booking.Booking) *booking.Booking {
	rctx, cancel := context.WithTimeout(ctx, calReadbackPageWait)
	defer cancel()
	if !h.followCalendar(rctx, b.ID) {
		return b
	}
	if fresh, err := h.bookingSvc.Get(ctx, b.ID); err == nil {
		return fresh
	}
	return b
}

// followCalendarSweep reads upcoming bookings' events back, at most once per
// calReadbackInterval. It runs from the reconciler, after the passes that push Bookmeet's
// own pending changes out, so it never reads an event Bookmeet is about to move.
func (h *Handler) followCalendarSweep(ctx context.Context) {
	now := time.Now()
	if last := h.calReadbackAt.Load(); last != 0 && now.Sub(time.Unix(0, last)) < calReadbackInterval {
		return
	}
	h.calReadbackAt.Store(now.UnixNano())

	// Read the ids fully before acting: the single DB connection cannot serve the
	// per-booking queries while this cursor is open.
	rows, err := h.db.QueryContext(ctx, `
		SELECT b.id FROM bookings b JOIN booking_hosts bh ON bh.booking_id = b.id AND bh.is_primary = 1
		WHERE b.status = 'confirmed' AND b.end_at > ? AND b.start_at < ?
		  AND COALESCE(bh.external_event_id, '') != '' AND bh.needs_sync = 0
		ORDER BY b.start_at ASC LIMIT ?`,
		now.UTC().Format(time.RFC3339), now.Add(calReadbackHorizon).UTC().Format(time.RFC3339), calReadbackBatch)
	if err != nil {
		h.logger.Error("calendar read-back: query upcoming bookings", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close() // #nosec G104 -- rows already fully consumed above; nothing actionable on close error

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
		h.followCalendar(ectx, id)
		cancel()
	}
}

// stampEventAccount records which connected account wrote a host's calendar event, so a
// later read-back can tell "deleted" from "lives in an account that is no longer connected".
func (h *Handler) stampEventAccount(ctx context.Context, bookingID, userID, provider string) {
	gc := h.getCal()
	if gc == nil || provider == "" {
		return
	}
	if _, err := h.db.ExecContext(ctx,
		`UPDATE booking_hosts SET external_account = ? WHERE booking_id = ? AND user_id = ?`,
		gc.DestinationAccount(ctx, userID, provider), bookingID, userID); err != nil {
		h.logger.Error("save event account", "error", err, "booking_id", bookingID, "host", userID)
	}
}
