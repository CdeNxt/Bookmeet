# Calnode — agent notes

Go backend (SQLite, `go:embed`s the built SvelteKit SPA) + SvelteKit 5 admin UI
served under `/admin/`. Public booking pages are server-rendered Go templates
(`internal/handler/templates/*.html`), distinct from the Svelte admin app.

## Frontend toolchain

- Vite 8 (Rolldown) · SvelteKit 2 · Svelte 5 · Tailwind v4 (`@tailwindcss/vite`)
  · bits-ui / shadcn-svelte · Vitest 4 browser mode.
- The frontend is embedded at Go compile time (`frontend/embed.go` →
  `//go:embed all:build`). To see frontend changes in the running app: rebuild
  the frontend (`pnpm build` in `frontend/`) **and** rebuild/restart the Go
  binary — restarting Go alone won't pick up new assets.

## UI styling — required check

**Default to shadcn-svelte in the admin UI.** In the SvelteKit admin app (`frontend/`),
build from the existing shadcn-svelte components — `Button`/`buttonVariants`,
`ConfirmDialog` (**never** `window.confirm`/`alert`), `Dialog`, `Input`, `Switch`,
`Tooltip`, etc. Don't hand-roll buttons, modals, or browser-native dialogs. Destructive
actions use `ConfirmDialog` with `destructive`; row actions use a ghost icon button +
`Tooltip` (see `event-types`, `members`, `recordings`). **If shadcn genuinely doesn't fit,
flag it (and the reason) before deviating — don't silently hand-roll.** This does NOT apply
to the public booking templates (`internal/handler/templates/*.html`), `embed.js`, or the
LiveKit room — those are intentionally framework-free (Go templates / vanilla JS, own CSS).

shadcn-svelte components style state via Tailwind `data-*` variants. Bits-ui
states exposed as `data-state="…"` (checked/unchecked/open/closed) need an
`@custom-variant` remap in `frontend/src/app.css` or they render **silently
unstyled** (logic works, visuals don't). See `frontend/TESTING.md`.

**After changing `frontend/src/lib/components/ui/**`, `frontend/src/app.css`, or
the theme — run `pnpm test:visual`** (Vitest browser smoke). Unit tests do NOT
catch this class of bug; only the real-browser computed-style assertions do.

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

## Built-in video meetings (LiveKit)

Self-hostable video as a booking location type (`location_type = "livekit"`). **No LiveKit SDK
server-side** — all tokens are hand-signed. The browser room app is **vanilla JS + a vendored
client SDK**, not Svelte.

- **Where it lives:** room UI = `internal/handler/templates/livekit-room.html` +
  `assets/livekit-room.js` (+ vendored `assets/livekit-client.umd.min.js`). Server =
  `internal/livekit/` (`livekit.go` token signing, `admin.go` Twirp/egress) and
  `internal/handler/livekit_room.go` + `livekit_recording.go`. Settings UI =
  `frontend/src/routes/settings/video/`.
- **Three token kinds (don't conflate):** (1) **room token** — opaque HMAC blob in the join URL
  (`{r,e,role}`), carries no LiveKit grant; (2) **access token** — the real LiveKit HS256 JWT the
  SDK joins with (`AccessToken`/`VerifyAccessToken`); (3) **admin token** — short-lived JWT for
  Twirp server APIs.
- **Host authority — `authorizeHost`, NOT just the room token.** A host action is allowed if the
  caller is the **durable host** (`hostRoomOrOwner`: holds a host room token OR is the signed-in
  booking owner) **OR** the **current reassigned host** — proven by verifying their *access token*
  and confirming that identity has `metadata="host"` right now (`ListParticipants`). Clients send
  **both** `t` (room token) and `at` (access token) on every host call. **Reclaim host is
  durable-host-only.** Reassigning only flips metadata, so without the access-token path a temp
  host has the badge but no real power — that gap is exactly what `authorizeHost` closes.
- **Single host:** any host join demotes prior hosts (`demoteOtherHosts` → metadata `"attendee"`);
  the client downgrades only on explicit `"attendee"`, never on a transient/empty metadata event.
- **Recording (Egress):** room-composite → the **Litestream backups bucket** (`LITESTREAM_*` env),
  `recordings/` prefix. **Finalize on stop/end (`finalizeActiveRecording`), do NOT depend on the
  webhook** — `object_key` is set at start so downloads work without it; a startup sweep closes
  orphaned `active` rows. Idempotent guard keys on an `active` row per room.
- **Webhook = single sink** `POST /v1/livekit/webhook` (legacy alias `/v1/livekit/egress-webhook`,
  keep it). LiveKit allows one URL per project, so it receives **all** events; we verify the
  signature and act only on `egress_started/ended/failed` + `room_finished` (everything else is
  200-ACKed and dropped). The **egress lifecycle is the source of truth** for the recording flag.
- **Recording banner** is driven by room **metadata** (`{recording, allowShare}`) + the
  `RoomMetadataChanged` realtime event — it's an in-room overlay, so `showOnly` hides it off the
  room view. **Always `mergeRoomMeta` (read-merge-write), never overwrite** — recording and
  screen-share flags would clobber each other.
- **Attendee screen-share defaults OFF**; host opts in (gear menu). Enforced server-side via
  `canPublishSources` at token mint + live `UpdateParticipant`, not just hidden in the UI.
- Room HTML is served `no-store` and injects `?v=<content-hash>` onto the room JS/SDK assets —
  bump-free cache-busting. After changing the room JS/HTML you still need a frontend-independent
  Go rebuild (these assets are `go:embed`-ed in the handler package, not the SPA).
- **Watch the room JS complexity.** `livekit-room.js` has grown large + stateful (host model,
  single-host, consent, chat, layout, recording) with state in scattered module flags + manual
  DOM updates — several bugs traced to that (stale state, the metadata up/down-grade logic). It's
  fine now, but if it keeps growing the move is NOT "shadcn-ify it" (it's deliberately
  framework-free) — it's tidy-in-place: one state object + a single derive/render, extract the
  pure logic (host/consent state machines) into testable functions. A dedicated tiny Svelte build
  is the last resort, not the first.

## Languages (i18n) - data-driven, adding one touches no code

Public surfaces (book.html, manage.html, embed.js, the four emails, calendar invite
title/description) are translated. **NOT translated: the admin SPA, the LiveKit room**
(`livekit-room.html`/`.js`, ~45 hardcoded strings - it has its own asset pipeline and shares
no string plumbing with the Go templates), and admin-authored content (event names,
descriptions, questions, custom email copy). Locale is resolved per request from
`Accept-Language` + a `?lang=` override + the operator's fallback setting
(`internal/handler/i18n.go`), and the booker's locale is stored on the booking so later
reminders match. Ships `en es fr fr-CA de it pt nl sv`. Full detail: ARCHITECTURE §23.

**Adding a locale = adding `internal/i18n/locales/<code>.json`.** Nothing else. `init()`
globs the directory; the switcher, the fallback dropdown and the public API payload all read
`SupportedLocales()`. Name the file **BCP-47 canonical** (`pt-BR.json`, never `pt-br.json`).

- Go has no locale date data, so `date_format`, `clock_format`, `dow_short_*` and
  `month_short_*` are **keys in the JSON** (`internal/i18n/datetime.go`), not stdlib calls.
- Three guards police a new file: same-keys, printf-verb parity (uses `fmt` as the oracle),
  and `TestDateTablesMatchCLDR`, which cross-checks the date tables against `Intl` via node.
  Run `go test ./internal/i18n/`.
- Tests use `ja`/`ko` to mean "a language we don't ship". If you add either,
  `assertUnsupported`/`requireUnsupported` fail loudly and tell you what to change.
- **Every non-English locale is an LLM draft with no native review.** Structure is verified;
  wording is not. Say so before anyone markets a language.

### Where a translated sentence gets assembled

**Default: server-side.** Go composes the finished sentence and the page renders it as
given. `durationLabel`, `hostsLabel`, `locationLabel`, `assistantGreeting` and
`noticeLabel` (`book.go`) are the pattern - they return text, not parts. Keeping it there
is what stops plural rules, duration wording and date formats from being reinvented in
three separate front ends, only one of which has tests.

**One exception, and it is the only one:** a booking-surface string whose argument is
chosen by the visitor *after the page loads* may be substituted client-side, via
`BookingLogic.fmt` (book/manage) or its deliberate mirror in `embed.js`. Today that is
exactly the selected date, in `no_available_times`, `no_available_times_host` and
`min_notice_hint`. The alternative is a server round-trip on every calendar click, or
shipping a month of pre-rendered sentences to render one.

The line that still holds inside the exception: **anything locale-dependent is computed
in Go and passed in as a finished fragment.** `MinNoticeLabel` is the model - the server
sends "4 hours" already pluralised and translated, and the page only drops it into a
slot. `fmt` handles `%s` and the indexed `%[n]s` (so a translation can reorder its
arguments) and nothing else; it is not a printf and must not become one.

**If you are building a plural form, a duration or a date format in JavaScript, you have
crossed the line** - move it into `book.go` and send the result.

## Email - two transports, and the SMTP trap

`internal/mailer` has **two** real transports behind one `Mailer` interface: `smtp.go` and
`resend.go` (HTTPS to `api.resend.com`). **`handler.BuildMailer` is the ONLY place the
choice is made** - boot (`server.go`) and settings-save both call it, so they cannot drift.
The rule: a Resend API key selects HTTPS, else an SMTP host selects SMTP, else `Noop`.

- **Do not turn this into probe-and-fallback.** It was considered and rejected: a probe
  tests reachability at boot rather than at send time, an open TCP port is not a working
  delivery path, and silent switching masks a broken SMTP config while making "which path
  sent this?" unanswerable. Credentials state intent.
- **Why the HTTPS path exists at all:** several platforms (Railway below Pro) block
  outbound SMTP by *dropping* packets. It presents as a hang, then as a credentials
  problem, on every SMTP port, for every provider. Nothing at the SMTP layer fixes it.
- **Keep the dial bounded.** `defaultSMTPTimeout` must be on the dialers (`newDialers`),
  not only on `conn.SetDeadline`, which runs after the dial. Unbounded, a packet-dropping
  host hangs ~2 min and stalls the job queue, which shares the single SQLite connection.
- **`.ics` invites carry `method=REQUEST` in their Content-Type** - that parameter is what
  makes clients show RSVP instead of a file. The Resend path sets `content_type`
  explicitly; if invites ever arrive as plain attachments, look there first
  (`TestICSAttachmentKeepsItsMethodParameter`).
- Adding a secret to email settings: use `storeEmailSecret`, and make the JSON field a
  **pointer** so "omitted" (keep) stays distinguishable from `""` (clear).

## Event types: slot interval is not the duration

`slot_interval_minutes` = how often a booking may **start**. `duration_minutes` = how long it
**runs**. Deliberately independent (a 45-min meeting offered on the hour is valid). New event
types default the interval to the duration; existing ones keep what is stored, so a change of
default is never retroactive. `slots.Generate` refuses a non-positive interval, so create and
update both validate it - an unvalidated `0` yields an event type with no bookable times and
no explanation. Reported as issue #13, where the setting being absent from the admin UI looked
exactly like duration being ignored.

Keep the editor's floor aligned with the API's (`>= 1`). A stricter client-side minimum makes
an event type configured below it via the API unsaveable from the editor, even when the person
is editing an unrelated field.

**The general rule, learned twice:** the admin editor submits the WHOLE form on every save,
so validating a field merely because the request mentions it validates fields the operator
never touched. Any event type holding a stored value the current rules reject then becomes
unsaveable entirely, with an error pointing at something unrelated. Validate on **change**
(effective value vs stored), not on **mention**. Rows reach those states legitimately: a
provider disconnected after the fact, a duplicate that inherited one (#22), a seeder writing
straight to the table, or a create path that defaulted the field before a rule tightened.

The other half of the same rule: **anything written without validation must be valid by
construction.** `CreateEventType` skips `validateLocation` when it defaults the location,
because there is no request field to blame an error on - so every branch of
`smartDefaultLocation` has to return a type the owner can actually host at. It used to end
at an unconditional `"zoom"`.

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

## Personal booking links (Bookmeet; the personal booking assistant's foundation)

A personal link is made by an owner for one named guest, limited to windows the owner chose
(each day its own), usable once. Data layer: `internal/personallink` (tables `personal_links`,
`personal_link_windows`, migration 00083). Where it meets the shared cores:
`internal/handler/personal_link.go`. The assistant, Telegram and the guest chat page are not
built yet; the agreed design is in the owner's vault, `Feature Requests/personal-booking-assistant.md`.
The naming is "personal link" throughout because `invites.go` already means calendar invites.

- **The windows are a filter on what the engine found, never a second source of availability.**
  `personalLinkSlots` calls `computeSlots` and drops what the windows do not cover;
  `bookPersonalLink` calls the booking core. Do not compute free time for a link anywhere else.
  It asks `computeSlots` for no optional outputs, like the other agent-facing callers.
- **Single use is `Claim`, not a status check.** Claim is one guarded UPDATE that succeeds for
  exactly one caller; `bookPersonalLink` claims before it books, releases if the booking fails
  and marks booked when it succeeds. Reading the status first and booking second is the race
  this exists to prevent. A claim older than two minutes is treated as a dead attempt.
- **`outsideWindows` skips the window check and nothing else.** It is for a time the guest
  suggested and the owner approved. The time must still be bookable. Booking over a calendar
  clash on the owner's say-so is a later step and needs its own path.
- **Expiry is applied on read** (`load`), and not to a link in `proposed`: suggested times can
  fall after the offered windows, and the owner's answer must still be able to book.
- **The URL token is stored twice on purpose:** `token_hash` to find the link, `token_enc` so
  the owner can be shown the same link again when a guest loses it.
- **In person:** the link's confirmed address goes through `createBookingForSlugWith`, which
  follows `CreateBooking`'s in-person handling step for step. Keep the two in step. It needs
  an event type with `allow_in_person`.
- Not handled yet: event types with required intake questions (the link books with no
  answers), and inactive event types (`loadBookableEventType` refuses them). An unlisted
  one (`is_public = 0`) is bookable by link: `is_public` means listed, not bookable.

## Personal booking assistant: switch and Telegram connection (Bookmeet)

Settings → AI has a second card for the assistant. Server side: `assistant_settings.go` and
`internal/telegram` (a hand-rolled Bot API client). What the assistant does with a message
or a button tap is the next section.

- **The bot answers only linked chats.** A chat is linked by spending a one-time code made in
  Settings (`/start <code>`). Strangers, groups and unlinked chats get no reply at all, not
  even a refusal. Never add a reply path that runs before the `telegram_links` lookup.
- **A delivery is Telegram's only if it carries the registered secret**
  (`X-Telegram-Bot-Api-Secret-Token`, compared in constant time). Without it the webhook
  answers 404; with it, always 200, because Telegram re-sends what it is not thanked for.
- **The bot token is in every Bot API URL, and Go's transport errors quote the URL.**
  `telegram.Client.call` strips it; do not log or return a raw error from a new call site
  that bypasses it (`TestClient_errorsNeverCarryTheToken`).
- **A key is stored only after Telegram accepted it and the webhook was registered**
  (`connectTelegram`). The JSON field is a pointer: omitted keeps the key, `""` disconnects.
- **"Enabled" is not "active".** `assistantActive` also needs a connected bot and a live AI
  model, either of which can go away after the switch was flipped. Switching on is validated
  on change, not on mention, so re-saving an already-on switch is never blocked.
- **`TELEGRAM_WEBHOOK_BASE_URL`** names the address Telegram delivers to when the public one
  is behind a login Telegram cannot pass. Staging needs it: `staging.bookmeet.date` sits
  behind Cloudflare Access, so point it at the `fly.dev` address.
- v0 uses one dedicated bot per copy. Opening this to customers needs one shared bot plus a
  relay (owner's vault, DECISIONS 2026-11-11); keep Telegram behind `getTelegram()` so that
  swap stays a plumbing change.

## Personal booking assistant: the owner's side on Telegram (Bookmeet)

`assistant_owner.go`. The owner messages the bot; the model works out a guest, a meeting
type, days and times; the owner confirms on a details panel. The guest's side is the next
section.

- **The model's only way to act is `show_details`, which shows a panel. Creating a link and
  booking happen on a button tap, never from model output.** A tap is callback data handled
  by `tapDraft`; no model reads it. Do not add a tool that creates, books or cancels by
  itself: `cancel_link` only asks, and the tap cancels.
- **Everything the model hands over is validated in `toolShowDetails` before the owner sees
  it,** and a refusal is returned to the model as "error: ... ask the owner ...", so it asks
  instead of showing a broken panel.
- **Dates come from a calendar table in the prompt, and times stay in the owner's zone on
  the draft** (`draftWindow`: a date plus minutes). They become instants once, in
  `createLink`. Do not have the model compute dates or offsets.
- **One exact time:** with the guest's email it is a direct booking (`bookDirect`, through
  `createBookingForSlugWith`, no personal link); without one it becomes a link whose single
  window is exactly the meeting's length. The owner gets the booking's manage link.
- **Places:** the model passes `place_query`; `searchPlaces` (Google, the same key as the
  booking page) returns options and the owner taps one. The model never writes an address.
  Without a Places key the place is taken as typed and the panel says it is unchecked.
- **One live panel per owner.** A new panel strips the buttons from the old one; a tap on a
  closed panel answers "no longer active". Work for one owner is serialised (`ownerLocks`).
- **Assistant work runs off the webhook request** (`assistantRun`), so Telegram is answered
  at once. Tests set `assistantInline`.
- The prompt (`ownerAssistantRules`) is code-owned, like `assistantBaseRules`.
- Not built: a meeting topic on a direct booking (bookings have nowhere to keep one), and
  per-meeting length or location.

## Personal booking assistant: the guest's chat page (Bookmeet)

`GET /i/{token}` (`templates/plink.html`) is a shell; the conversation comes from
`POST /v1/links/{token}/chat` (`personal_link_chat.go`) and is saved on the link
(`personal_link_messages`), so reloading continues it.

- **The program owns the flow; the model fills the gaps.** The greeting, the order (email,
  then phone, then times), the cards and every button are the program's. A button press
  never reaches the model. A typed email or phone number is read by the program too
  (`heard`), so the common path works with no model at all.
- **The model gets tools for the current step only** (`tools`), and each ends in something
  the program draws. `pick_time` shows a confirm card; only the guest's Confirm books, through
  `bookPersonalLink`. When a tool put something on screen, the model's closing remark is
  dropped so it cannot contradict the card.
- **The model's state is the link and the open times inside its windows, and nothing before
  the details are done** (`prompt`). No calendar contents, no other bookings, no other
  guests. Keep it that way: what it is never told, it cannot be talked into revealing.
  `TestPersonalLinkChat_theModelSeesOnlyTheLinkAndCanOnlyPickAnOfferedTime` pins this.
- **Times are in the guest's own zone,** reported by their browser and sent with every
  request. Confirm and booked cards show the owner's time alongside when the zones differ.
- **The page caps what it shows** (three days, five times each) behind "More times". That is
  the page's choice; the server sends every open time.
- **A booking tells the owner on Telegram at once** (`notifyOwnerBooked`), from the booking
  record, not from the model.
- **The booked card only claims an invitation when one will arrive** (`guestWillBeEmailed`:
  Bookmeet's email is set up, or the host's calendar invites the guest). Otherwise the card
  and the owner's message say no email is coming. The program then has the last word: the
  guest is told they can close the chat.
- **The owner's celebration effect plays on a fresh booking,** the same way the booking page
  does it: the page fires `bookmeet:booked` and `bookmeet-effects.js` plays the branding
  setting. Not on reopening a link that is already booked.
- The module's switch governs links too: off, they stop opening.
- **Not this page's job, and not a fourth calendar surface:** it has no month grid. Do not
  grow one here; the three calendar surfaces stay three.
- **The guest's wording is translated; the owner's and the model's is not.** Everything the
  guest reads comes from `plink_*` keys in the locale files through `plinkSession.t`, with
  the locale's own day names and clock (`FormatDay`, `FormatTimeOfDay`), composed in Go as
  everywhere else. The page is handed only the few labels it draws (`plinkPageKeys`).
  Telegram messages to the owner and the model's prompt stay English (`whereIn(i18n.Default())`,
  `plinkClock`, `zoneLabel`): do not route them through `t`. The guest's language is kept
  on the link (`guest_locale`) for a card written when they are not there, and on the booking.
  The page loads `/booking.css` (`.asst-*` and `.plink-*`) and the public skin.
- Not built: intake questions asked in chat.

## Personal booking assistant: what the owner hears, and booking over a clash (Bookmeet)

`personal_link_report.go`, with the held items and chat state in `internal/personallink`
(`personal_link_held`, `chat_mode` and the three `chat_*_at` columns, migration 00087).

- **Two rules decide when the owner is told.** A booking reaches them the moment it happens
  (`notifyOwnerBooked`). Everything else the guest wanted passed on - suggested times, a
  change request, a note, "I'll follow up", "I can't make it" - is held (`Hold`) and sent as
  ONE message when the chat is over (`reportChat`). Do not send the owner anything else from
  inside a live chat.
- **A chat is over when** the guest says so (`end`, reported at once), their page has been
  closed for 45 seconds (a `leave` beacon, then the sweep), or they have been quiet for five
  minutes (`ChatsOver`, run every minute by `StartPersonalLinkSweep`). A page closing cannot
  always be seen, which is why quiet is the backstop. On a machine that sleeps when idle the
  sweep sleeps too, and the message goes out when it next wakes.
- **Every message names the meeting briefly** (event type, and the time once booked): an
  owner may have several with the same person.
- **Nothing the owner does goes back into the guest's chat.** A tap books a suggested time
  (the guest gets a calendar invitation, and a booked card waits for when they reopen the
  link) or changes the offer (the owner gets a new link to send). "None of these" sends the
  guest nothing.
- **Every suggested time reaches the owner, with a verdict for their eyes only**
  (`suggestedTimeVerdict`: free, clashes with the calendar, outside their hours, taken by
  another Bookmeet booking). The guest and the model are never told which, or a suggestion
  would be a way to probe the calendar. The verdict asks `planBooking`, the same checks a
  booking makes, so it cannot disagree with what booking would do.
- **`bookingExtras.OwnerApproved` is the one path that books over the owner's own rules and
  calendar,** and it exists only behind the owner's "Book anyway" tap
  (`bookPersonalLinkApproved`). Never set it from a guest's action, a model's output or an
  agent's call. It still refuses a past time and another Bookmeet booking in the way.
- **`errCalendarClash` reads and matches as `errSlotUnavailable`** on purpose: code that
  words an error for a booker or an agent cannot tell a calendar clash from any other
  unavailable time. Only `errors.Is(err, errCalendarClash)` learns the reason.
- **What the guest's note says is what they typed** (`lastText`), not the model's retelling.
- A held item is answered once (`ResolveHeld`); a second tap is told so.

## Personal booking assistant: exceptions to the owner's availability (Bookmeet)

The owner can let one meeting go outside their own rules. Everything here is behind
Settings → AI → "Allow exceptions to my availability" (`assistant_exceptions`, on by
default), and behind a tap each time. Off, the assistant only reports.

- **How far ahead the owner takes bookings (`max_future_days`) never applies to a meeting
  they arranged** - a personal link, a direct booking, a guest's suggestion they book. It is
  a rule for the public booking page. `slotsWanted.OwnerArranged` and
  `bookingExtras.OwnerInvited` drop it; the panel no longer warns about it. A link that
  was refused for it showed the guest "I'm having trouble" (the engine rejects a date range
  wholly beyond the horizon), which is how this was found. Hours and notice still need the
  Allow tap.
- **Three separate waivers, and only the owner sets any of them** (`bookingExtras`):
  `OutsideRules` sets aside the owner's hours, minimum notice and booking horizon and KEEPS
  the calendar check; `OwnerApproved` also books over what the calendar shows;
  `OverBookings` (with `OwnerApproved`) lets it overlap another Bookmeet booking. Never set
  one from a guest's action, a model's output or an agent's call.
- **An exception on a link is `OutsideRules`, not more.** `personal_links.outside_availability`
  makes `personalLinkSlots` ask the engine with `slotsWanted.OwnerException` (every hour of
  every day, no notice, no horizon) and then narrows to the link's windows as always. A time
  the owner is busy for is still left out and still does not book. No public or
  agent-facing caller may set `OwnerException`.
- **The panel checks before it creates** (`analyse`, via `windowSlotCounts`): for each window,
  how many times the engine offers as things stand and how many with the rules set aside. It
  says which windows are outside the owner's hours, beyond their booking horizon, or fully
  busy. Nothing that is wholly outside is created without the Allow tap. `analyse` reads the
  calendar, so it runs when the times change, not on every redraw.
- **`OwnerInvited` lifts the per-guest booking limits** for a booking the owner asked for (a
  personal link, a direct booking, an approved suggestion). Those limits stop guests
  over-booking themselves; they were blocking the owner's own invitations.
- **Conflicts are named for the owner only** (`timeVerdict.Conflicts`): another Bookmeet
  booking by event type and guest, a calendar event by its title
  (`calendar.BusyEventLister`, implemented for Google; other providers say "something on your
  calendar"). A title is the only calendar content this program reads. It goes into the
  owner's Telegram message and nowhere else: not the guest's chat, not a model's prompt, not
  `assistant_messages` (`reportChat` keeps the named lines out of what it remembers).
- **Booking over another Bookmeet booking** needs `CreateParams.AllowOverlap`. Two bookings
  for one host still cannot start at the same instant (the unique index), and the owner is
  told when that is the reason.

## Personal booking assistant: links that run out, and the admin list (Bookmeet)

- **A link that runs out unused is announced to the owner once** (`sweepRanOut` →
  `tellRanOut`, on the same one-minute sweep as the chats): which meeting, how far the guest
  got, and a "New link" button. `MarkExpiryTold` is the once-only guard; it is taken before
  the message is sent. A link that ran out more than three days ago is marked and never
  announced, so connecting Telegram late does not bring a pile of old news. The button
  (`l|n|<id>`) reopens the offer in the panel or asks for new times; it creates nothing.
- **Admin → Personal links** (`personal_link_admin.go`, `frontend/src/routes/personal-links`)
  is the same log, read-only apart from Cancel. The list never carries a token: the address
  is fetched for one link on a click (`GET /v1/personal-links/{id}/url`) and only while the
  link still works. Making and changing a link stays with the assistant, where the offer is
  checked against the owner's availability first. The nav entry shows when the assistant is on.
- **Limits:** the page 60 a minute and the chat 40 a minute per address, 80 typed messages
  per link, and 20 held items per link (`personallink.MaxHeld`; past it the guest is told to
  contact the owner). Details panels older than two weeks are deleted (`tidyDrafts`).

## The assistant lab (`cmd/lab`): testing the AI features without staging

`go run ./cmd/lab` builds the real app from the checkout, runs it on a throwaway database
at `localhost:3067`, and opens a bench at `http://localhost:3066`. `go run ./cmd/lab run
[-n 3] [name…]` runs scripted conversations against it. It is a development tool: nothing
in the app imports it and it is not in any image. Everything it writes is in `.lab/`
(ignored by git).

- **The prompts, tools and guardrails under test are the real ones.** Only two things are
  stand-ins. Telegram: the app's bot calls go to the lab (`TELEGRAM_API_BASE_URL`), which
  delivers messages and taps to the real webhook with the real secret. The route to the
  model: the app's model endpoint is the lab, which forwards each request unchanged to the
  address entered on the lab page and records both sides (bodies only, never the key).
- **`TELEGRAM_API_BASE_URL` is the lab's one hook in the app.** Set from configuration it
  also lifts the "webhook must be https" rule (`telegramStandIn`), because that rule is
  Telegram's. Leave it unset everywhere real; the app logs a warning at boot when it is set.
- **The world is set up through the app's own API** (claim, hours, meeting types, bot,
  chat link, bookings) so it is a state a real owner reaches. Two things are written
  directly because no API exists: a personal link (only the assistant makes those) and the
  assistant's switch when no model is set. Each scenario run starts from a snapshot of that
  world; nothing carries over between runs.
- **Other people's data is marked.** The seeded bookings and link belong to guests with
  marker names (`canaries` in `lab.go`). Every guest scenario fails if a marker reaches the
  guest's page or the model on a guest's behalf, if a booking lands outside the link's
  windows or under another email, or if someone else's booking or link changed.
- **Check facts, not phrasing.** A scenario's `expect` is counts and states (bookings,
  links, held kinds, stage, cards shown); wording checks are few and loose because the
  model's words vary. Run with `-n` and read the pass rate.
- **Scenario files live in `.lab/scenarios/` and are not in the repository** (it is public;
  J has not said the attack scripts may be). `cmd/lab/example-scenario.json` shows the
  format. A failure worth keeping becomes a scenario there, and where it can be pinned
  without a model, a Go test as well.
- The lab does not cover real Telegram delivery, Google Calendar or email. Staging does.

## Conventions

- `pnpm` (not npm). Use `pnpm exec <tool>` for local binaries.
- Verify changes against the real app, not just builds — this codebase has been
  bitten by CSS that compiles fine but renders wrong.

## Distribution packaging (packaging/)

- One channel per directory: `packaging/ns8/` is the NethServer 8 module
  (imageroot actions, systemd unit, Vue settings UI, robot tests). Future
  channels (Umbrel, Coolify, TrueNAS, etc.) get siblings, never a mega-PR.
- The NS8 module is a thin wrapper around the published app image, built by
  `.github/workflows/ns8-module.yml` with the same tag scheme as
  `docker-publish.yml`. Release tags pin module and app to the same version.
- The app image ref lives in the `org.nethserver.images` label; at install
  the cluster exposes it as `${CALNODE_IMAGE}` and the unit + configure read
  it from there. Never hardcode an app tag in `packaging/ns8/`.
