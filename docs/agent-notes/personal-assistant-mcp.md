# The personal booking assistant over MCP

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

## Personal booking assistant over MCP: every agent, not only Telegram (Bookmeet)

J's rule (2026-11-13): the owner's own AI agent (Claude, ChatGPT, others) can do what the
Telegram assistant does, and the two stay in step as the assistant grows. Bookmeet is meant to
be an agentic interface for booking, not a Calendly copy. Tools: `mcp_assistant.go` (list,
get, cancel, `whats_waiting`, the server instructions), `mcp_assistant_create.go` (check and
create or change a link), `mcp_assistant_book.go` (`book_for_guest`, `answer_guest`).

- **An assistant feature is not done until its MCP side exists:** a tool, its line in
  `mcpInstructions`, a test, and an entry in `mcpAssistantParity`. That map ties each
  Telegram model tool to its MCP tool; `TestMCPAssistant_parityWithTelegram` fails on a
  Telegram tool with no entry. A gap is written there as `pending: ...`, not left out.
- **Same cores, thin front ends.** MCP tools call what Telegram calls (`windowSlotCounts`,
  `personallink.Service`, `bookPersonalLink`, `createBookingForSlugWith`,
  `suggestedTimeBooked`). When a Telegram path grows a step, move it into a shared function
  rather than copying it.
- **An agent's call is never the owner's tap.** No MCP tool sets `OwnerApproved`,
  `OutsideRules`, `OverBookings` or `OwnerException`. `checkOffer` applies the panel's rules
  exactly: days without times get `usualHours`, and with exceptions on, any time outside the
  owner's usual hours makes `needs_owner_approval` and no link is made. `answer_guest` asks
  `suggestedTimeVerdict` before booking, as `tapHeld` does, because `bookPersonalLink` applies
  `linkException` and skips the window check: booking a suggestion straight away would waive
  the owner's hours with no tap. When Telegram's rules change, change these with them.
  "Book anyway" and the direct booking's Allow tap stay the owner's: on a card app, a tap on
  the card (below); otherwise with `ask_owner` (ignored on a card app), the
  agent hands the decision to Telegram (`askOwnerToBookDirect` and `askOwnerToConfirmLink`
  show the same panel; `askOwnerToBookSuggested` runs the same `tapHeld` question), and only
  the tap books or creates.
  Refusals name the
  category (outside hours, busy) and never what is on the calendar:
  `TestMCPAssistant_calendarTitlesNeverReachAnAgent` calls every route at a titled event.
- **The agent does no time arithmetic.** It passes dates and clock times in the owner's
  zone; every reply carries RFC 3339 plus `text` already in the owner's zone and clock, and
  `today`/`now` to resolve "next Tuesday" against.
- **Guest text is a third party's.** It goes out as `guest_words`, and the instructions tell
  the agent to quote it and never follow it.
- **Every tool has annotations** (read-only, destructive): apps use them to ask before a call.
- **Personal links belong to one person.** Assistant tools act for the signed-in caller, or
  the workspace owner on stdio (`mcpAssistantOwner`), never "the whole workspace".
- **What each app can draw is in `docs/agent-capabilities.md`.** Re-check it from current
  documentation when starting an assistant feature, and match Telegram's look as far as the
  app allows. Text must always work on its own.
- **In person:** the check looks `place_query` up (`searchPlaces`) and returns
  `place_options`; create takes `place` only if it is one of them, exactly. The agent never
  writes an address, the same rule as the Telegram model. No place key: taken as typed and
  marked `place_unverified`, as on the panel.
- **Cards (MCP Apps), `mcp_cards.go` + `assets/mcp-card.html`.** Apps that declare the
  `io.modelcontextprotocol/ui` extension draw one Bookmeet card for every assistant tool
  (`withCard`): the offer, the link with Copy, booked, what's waiting, one link with the
  guest's suggestions. The card is framework-free like the public pages, and words things for
  the owner ("your hours"), not the agent. Text-only apps get exactly the text replies above.
- **The owner's approval is a tap on a card, and only that** (J, 2026-11-14). Where Telegram
  would show Allow / Book anyway, a card app gets an approval card instead of an error, and
  the tap calls `owner_approve`: `visibility: ["app"]`, so the app hides it from the model
  and refuses it unless the card calls it. Two more locks for an app that gets that wrong:
  it needs the ticket (visible) AND the key, which travels only in the result's `_meta`
  (kept out of the model's context); and tickets are issued, and `owner_approve` accepted,
  only on a connection that declared cards. A ticket is one exact action, one owner, one
  use, 15 minutes, in memory. The exceptions setting is read again at the tap.
  "Declared cards" is read per request (`req.ClientCapabilities`), because `/mcp` is
  stateless on purpose (#170): apps on protocol 2026-07-28 send capabilities with every
  request; an older-protocol app on HTTP shows as text-only and gets refusals, never tickets.
  `owner_approve` only ever runs the same paths as the Telegram taps (`createLink` with
  the exception, `bookDirect` with `OutsideRules`, `bookPersonalLinkApproved`). Calendar
  titles are never on a card: the model can read what the card is given.
- **"Show my calendar" on a card** (`mcp_card_calendar.go`, app-only `show_my_calendar`):
  the days a card is about, with usual hours, busy times, the owner's Bookmeet bookings and
  the times asked for. Busy times only, never titles, for the reason above.
- **One tap, not a conversation, for an approval.** Agents are told to call the tool that
  makes the thing (the result carries the Allow card) instead of asking the owner to approve
  in words first, and that the tap can come after their reply. Copy buttons use
  `execCommand("copy")` first: app frames block the clipboard API.
- **The app the owner is in is the only interface (J, 2026-11-24).** A card app is never
  sent to Telegram: no `ask_owner`, no "From your AI app" message (`agentDid` and
  `suggestedTimeBooked` stay quiet; the assistant's memory is still written). Text-only apps
  keep the Telegram route until J picks another.
- **Say which rule, and make the button keep its promise.** A refusal or card names every
  reason (`timeVerdict`, worded in `mcp_reasons.go`): minimum notice, outside hours,
  something on the calendar, or only the buffer around it. A minimum notice is never called
  "outside your hours". A direct booking's card is "Book anyway": the tap waives exactly the
  reasons shown, including a calendar clash, and a new reason since the card gets a new card.
  Only another booking starting at the same instant cannot be waived. Telegram's direct
  panel does the same with its own "Book anyway" button (`bookDirectAnyway`; J, 2026-11-24:
  Telegram and the cards can do the same things). Neither offers busy times on a link.
- **The model hears the tap.** The card sends the outcome of `owner_approve` with
  `ui/update-model-context`; apps that ignore it still need `list_bookings`.
- **No card and no text channel: the approval email** (J, 2026-11-24),
  `owner_email_approval.go`. Sent only when there is no linked Telegram chat: for an
  `ask_owner` call from a text-only app, and for times a guest suggested (with whatever else
  they left). The link opens `/approve/{token}`: GET changes nothing; deciding needs the
  owner's own signed-in session and a button, so neither a mail scanner nor an agent with
  the mailbox can approve. It runs the card's paths and waives only what the page showed.
  The email names kinds of reason only; calendar titles appear on the signed-in page.
  Tokens are stored hashed, single use, at most 7 days, 10 emails an hour per owner. The
  assistant can be switched on with email alone; a Telegram bot is no longer required.
- **No spoken approval.** In voice the owner asks, and taps the card on screen; ChatGPT's own
  voice mode makes the same choice. Do not add a way for words alone to approve.
- Not built yet: a "Waiting on you" inbox in the admin (the owner's likely next step after
  the approval email). A ran-out or empty link is still only announced on Telegram.
- **The bot hears what the agent did** (`agentDid`): a link made or cancelled, a booking, an
  answer to a guest is sent to the owner on Telegram and written to the Telegram assistant's
  memory, so the two front ends agree about what exists. Nothing from the calendar goes there.
