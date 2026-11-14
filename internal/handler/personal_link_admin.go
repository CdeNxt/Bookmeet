package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/calnode/calnode/internal/personallink"
)

// The owner's personal links in the admin app: the same log the assistant reads on
// Telegram, to look through, copy a link from, or cancel one. It is read-only apart from
// cancelling. Making and changing a link stays with the assistant, which is where the
// offer is checked against the owner's availability before anything is created.

type personalLinkWindowJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type personalLinkJSON struct {
	ID            string                   `json:"id"`
	GuestName     string                   `json:"guest_name"`
	GuestEmail    string                   `json:"guest_email"`
	GuestPhone    string                   `json:"guest_phone"`
	EventTypeName string                   `json:"event_type_name"`
	Topic         string                   `json:"topic"`
	Place         string                   `json:"place"`
	Windows       []personalLinkWindowJSON `json:"windows"`
	Status        string                   `json:"status"`
	// Active: the guest can still use it, so it can be copied and cancelled.
	Active              bool   `json:"active"`
	OutsideAvailability bool   `json:"outside_availability"`
	BookingID           string `json:"booking_id,omitempty"`
	BookedStart         string `json:"booked_start,omitempty"`
	LastOpenedAt        string `json:"last_opened_at,omitempty"`
	ExpiresAt           string `json:"expires_at"`
	CreatedAt           string `json:"created_at"`
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ListPersonalLinks handles GET /v1/personal-links: the signed-in owner's links, newest first.
func (h *Handler) ListPersonalLinks(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()
	links, err := h.personalLinks().List(ctx, user.ID)
	if err != nil {
		h.logger.ErrorContext(ctx, "personal links: list", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	names := map[string]string{}
	if rows, err := h.db.QueryContext(ctx, `SELECT id, name FROM event_types WHERE user_id = ?`, user.ID); err == nil {
		for rows.Next() {
			var id, name string
			if rows.Scan(&id, &name) == nil {
				names[id] = name
			}
		}
		rows.Close()
	}
	out := make([]personalLinkJSON, 0, len(links))
	for _, l := range links {
		item := personalLinkJSON{
			ID: l.ID, GuestName: l.GuestName, GuestEmail: l.GuestEmail, GuestPhone: l.GuestPhone,
			EventTypeName: names[l.EventTypeID], Topic: l.Topic, Place: l.PlaceAddress,
			Status: l.Status, Active: l.Active(), OutsideAvailability: l.OutsideAvailability,
			BookingID: l.BookingID, LastOpenedAt: rfc3339OrEmpty(l.LastOpenedAt),
			ExpiresAt: rfc3339OrEmpty(l.ExpiresAt), CreatedAt: rfc3339OrEmpty(l.CreatedAt),
			Windows: make([]personalLinkWindowJSON, 0, len(l.Windows)),
		}
		// A claim is a booking in flight; to the owner that is "booked" a moment early.
		if l.Status == personallink.StatusClaiming {
			item.Status = personallink.StatusBooked
		}
		for _, win := range l.Windows {
			item.Windows = append(item.Windows, personalLinkWindowJSON{Start: rfc3339OrEmpty(win.Start), End: rfc3339OrEmpty(win.End)})
		}
		if l.BookingID != "" {
			var start string
			if h.db.QueryRowContext(ctx, `SELECT start_at FROM bookings WHERE id = ?`, l.BookingID).Scan(&start) == nil {
				item.BookedStart = start
			}
		}
		out = append(out, item)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// GetPersonalLinkURL handles GET /v1/personal-links/{id}/url: the address to send the guest
// again. It is fetched on its own, on a click, so a page of links is not a page of live
// tokens. Only a link that still works has one worth copying.
func (h *Handler) GetPersonalLinkURL(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	links := h.personalLinks()
	l, err := links.Get(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		h.writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !l.Active() {
		h.writeError(w, http.StatusConflict, "this link no longer works")
		return
	}
	token, err := links.Token(r.Context(), user.ID, l.ID)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "personal links: token", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.writeJSON(w, http.StatusOK, map[string]string{"url": strings.TrimRight(h.publicURL(), "/") + "/i/" + token})
}

// CancelPersonalLink handles DELETE /v1/personal-links/{id}. The link stops working; it
// stays in the list as cancelled. A link that was already used or closed is a conflict.
func (h *Handler) CancelPersonalLink(w http.ResponseWriter, r *http.Request) {
	user, ok := userFromContext(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	err := h.personalLinks().Cancel(r.Context(), user.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, personallink.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, personallink.ErrNotBookable):
		h.writeError(w, http.StatusConflict, "this link was already used or closed")
	case err != nil:
		h.logger.ErrorContext(r.Context(), "personal links: cancel", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
