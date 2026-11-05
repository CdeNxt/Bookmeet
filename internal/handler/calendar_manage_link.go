package handler

import (
	"context"
	"fmt"
)

// calendarBookingDescription is shared by initial event creation and retries.
func (h *Handler) calendarBookingDescription(ctx context.Context, bookingID, reference string) (string, error) {
	token, err := h.bookingSvc.IssueCalendarManageToken(ctx, bookingID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\n\nReschedule or cancel this meeting:\n%s/manage/%s\n\nOpen the secure link above, then choose Reschedule or Cancel booking. No account is required. Use this link rather than editing or deleting the Google event so the host is notified and booking availability is updated. Keep this link private.", reference, h.publicURL(), token), nil
}
