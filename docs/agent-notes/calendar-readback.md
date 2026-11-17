# Bookings follow the host's calendar

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

## Bookings follow the host's calendar (read-back)

`internal/handler/calendar_readback.go`: the primary host's event is read back
(`calendar.Service.ReadEvent`, providers implement the optional `calendar.EventReader`) when a
manage link opens and on a ten-minute sweep run from the reconciler. A moved event moves the
booking (`booking.FollowCalendarTime`, deliberately no availability or overlap check); a
cancelled or deleted event cancels it. **No emails** on either (the calendar notifies guests);
webhooks and reminders are updated.

- **Never treat a bare "not found" as deleted.** Providers return `EventMissing`; only
  `Service.ReadEvent` upgrades it to cancelled, and only when the account answering is the one
  stamped on the booking (`booking_hosts.external_account`) or the provider resolves the owner
  from the event id (CalDAV). Otherwise a host who swaps accounts loses every booking.
- **Bookmeet's own pending changes win.** A booking changed in the last two minutes, or a host
  row with `needs_sync = 1`, is skipped: the calendar still shows the old time and reading it
  would undo the reschedule.
- Any new place that writes `external_event_id` must also call `stampEventAccount`.
