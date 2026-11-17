# The public booking surfaces and the book-by-chat assistant

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

## Booking calendar — THREE surfaces, keep them aligned

The date/time-slot booking calendar exists in **three** places. A change to its
behaviour or markup must usually be made in all three, or they drift:

1. **Booking page** — `internal/handler/templates/book.html` (server-rendered Go template + vanilla JS)
2. **Manage page** — `internal/handler/templates/manage.html` (reschedule flow; same calendar/slots)
3. **Embed widget** — `internal/handler/embed.js` (Shadow-DOM Web Component on customer sites)

- **Styling is shared:** all three load `internal/handler/templates/booking.css`
  (served at `GET /booking.css`; the widget injects it into its shadow root). Change
  visuals **there**, once — don't re-style per surface.
- **Markup + JS are NOT shared** (Go template vs web component): the calendar render,
  slot picking, and the **mobile step-flow** (calendar → slots → form, with Back) are
  implemented separately in each. If you change calendar *behaviour*, update all three.
- Verify on **desktop and mobile** for each surface after touching the calendar.
- **Shared slot logic lives in `internal/handler/assets/booking-logic.js`** (tested with
  `node --test`): day grouping, time formatting, and the taken-slot merge. It is inlined
  into **book.html and manage.html only**, which are the two surfaces that load it.
- ⛔ **The embed widget does NOT load `booking-logic.js`, so `BookingLogic` is undefined
  inside it.** `EmbedJS` serves `embed.js` as its own standalone file, unmodified — it is a
  Shadow-DOM web component on a third-party page, with no build step and nothing to
  prepend the module for it. It therefore carries its own copies of the few helpers it
  needs (`dowLabels`, `dayKey`, `timeLabel`, `shortDay`, `ymd`), each commented as
  mirroring the shared one. Calling `BookingLogic.anything` from `embed.js` throws a
  `ReferenceError` on the customer's site, where no test here would see it. So: put logic
  the pages share in `booking-logic.js`, and when the widget needs it too, mirror it there
  deliberately and keep the two in step.
- **Taken/booked slots** (`show_taken_slots`, off by default) are computed by
  `slots.GenerateWithTaken` as the *difference* between a normal pass and one ignoring
  busy, which is what keeps out-of-hours and min-notice starts from being mislabelled as
  booked. Never feed them to MCP or the assistant - `computeSlots(..., includeTaken)`
  makes each caller say. See ARCHITECTURE §8.

## Conversational booking assistant (optional LLM layer)

The "Book by chat" assistant lives on **two** of those surfaces — `book.html` (floating
drawer + inline link) and `embed.js` (inline link only; no floating button, to avoid
colliding with host-site widgets). **Not** on the manage page (reschedule context — a
reschedule chat is deliberately deferred). Server side is one endpoint,
`POST /v1/event-types/{slug}/assistant` (`booking_assistant.go`): an LLM tool-loop that
drives `find_available_slots`/`book` over the **shared deterministic cores** (`computeSlots`,
`createBookingForSlug`) — never re-implement booking logic in the assistant. Invariants:
the LLM does NL→constraints only (never time math), sees only computed availability (never
raw calendar data), and `<think>` reasoning is stripped. Shared `.asst-*` styles are in
`booking.css`; the base prompt (`assistantBaseRules`) is code-owned, admins only append
"Additional instructions". Off by default — `getLLM()` nil → the picker is the fallback.
