# Personal booking links and the personal booking assistant

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

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

The owner can let a meeting go outside their own rules. Everything here is behind
Settings → AI → "Allow exceptions to my availability" (`assistant_exceptions`, on by
default). Off, links keep to the owner's usual hours and the assistant only reports.

- **Days without times get the owner's usual hours.** The model passes a window with only a
  date when the owner named days but no times; `toolShowDetails` fills it from
  `usualHours` (the weekly rules for that meeting type, with date overrides) and leaves out
  days the owner does not work. The model is told the usual week (`usualWeek`) but never
  invents times. Only days off and no times is an error that tells the model to ask.
- **Times the owner explicitly asks for outside those hours are confirmed first.** The panel
  flags them ("outside the times you take meetings") and Create is refused until the Allow
  tap. Once confirmed, the link's windows are the hours for that meeting: it offers every
  time inside them the owner is not busy for. `linkException` (link made so, OR the setting
  on now) feeds `slotsWanted.OwnerException` in `personalLinkSlots` and `OutsideRules` when
  it books, read each time, so turning the setting on fixes a link already sent.
- **A direct booking (one exact time) needs the Allow tap** outside the owner's hours too.
- **When the setting is what blocks a meeting, the owner is told before any guest is**
  (`exceptionsOffHint`): on the panel, when Create is refused, and in the empty-link message.
  Keep that true for any new path where the setting can leave a link with nothing to offer.

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
- **An exception on a link is `OutsideRules`, not more.** `slotsWanted.OwnerException` asks
  the engine for every hour of every day, no notice, no horizon, and the result is then
  narrowed to the link's windows as always. The windows filter the engine; they are never a
  second source of availability. A time the owner is busy for is still left out and still
  does not book. No public or agent-facing caller may set `OwnerException`; making it the
  default for an owner-made link is the owner's own setting.
- **No link is created with nothing to offer.** `analyse` (via `windowSlotCounts`) counts, per
  window, the times the engine offers with and without the owner's rules, and sets
  `Offerable` (what the link would really offer) and `Checked`. The Create tap runs `analyse`
  again, because the calendar may have filled, and refuses when the calendar could not be
  checked or `Offerable` is 0. The panel names fully busy days. `analyse` reads the calendar,
  so it runs when the times change and at the tap, not on every redraw.
- **A guest who still finds no times is never at a dead end.** `times()` → `noTimes`: the
  guest is asked for up to three times (suggest mode, the usual suggestion path), and the
  owner is told at once with a "New times" button (`tellLinkEmpty`). That is the one
  exception to "tell the owner when the chat is over": the guest's suggested times on such a
  link are reported straight away too (`reportChat` from `sendSuggestions`).
- **Empty links are found before guests find them.** Every tenth sweep (`emptyCheckEvery`),
  `sweepEmptyLinks` counts the open, unbooked links (`OpenUnbooked`) and tells the owner
  once per link (`MarkEmptyTold`, `empty_told_at`) when one has nothing left. Each count asks
  the engine and the calendar, which is why it is not every minute.
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
