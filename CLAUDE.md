# Bookmeet — agent notes

Bookmeet is a maintained fork of the open-source scheduler Calnode, being built into a
commercial, AI-native scheduling product. Go backend (SQLite, `go:embed`s the built SvelteKit
SPA) + SvelteKit 5 admin UI served under `/admin/`. Public booking pages are server-rendered
Go templates (`internal/handler/templates/*.html`), distinct from the Svelte admin app.

`origin` is the fork (CdeNxt/Bookmeet, **public**: never commit a secret, database or booking
link). `upstream` is Calnode/calnode. `bookmeet` is the integration and release branch.

## Start of a session

1. One session, one deliverable. Read the owner's vault first: `~/Documents/Obsidian Vault/10
   Ventures/bookmeet/CONTEXT.md`, then `TASKS.md`. Product direction is in `SAAS-READINESS.md`
   and `AI-FIRST-PRINCIPLES.md` there.
2. Work in your own worktree on your own branch. How, for each kind of task:
   [`docs/DEVELOPING.md`](docs/DEVELOPING.md).
3. Read the notes for the area you will touch (table below) before changing it.

## Git and worktree safety

- `~/Developer/Bookmeet` stays on `bookmeet`: review and releases only. Never develop there.
- `~/Developer/Bookmeet-ai` is the long-running AI branch, `booking-assistant-v0`.
- Everything else: a short-lived worktree and branch cut from `origin/bookmeet`.
- Changes reach `bookmeet` by pull request with passing checks. No direct pushes.
- Never switch the branch of a folder another session may be using. Never force-push, reset
  or delete a branch that is not yours. Never use a bare `git stash`.
- `git pull` before adding a migration: numbers are global, and two branches that pick the
  same number break staging. Migrations must keep existing data.

## Commands

- `make check`: gofmt, go vet, frontend types. Run while working.
- `make test`: adds every test that needs no browser.
- `make verify`: adds a fresh frontend build and the real-browser visual tests. What CI runs;
  run it before opening a pull request.
- `pnpm`, not npm (`pnpm exec <tool>` for local binaries). Go version from `go.mod`, Node 22.
- The frontend is embedded at Go compile time. To see a frontend change in the running app,
  rebuild the frontend **and** the Go binary.
- Verify against the real app, not only builds: this codebase has been bitten by CSS that
  compiles and renders wrong. `go run ./cmd/lab` runs the AI features locally.
- Never weaken or skip a test or check to get a green result. Fix the cause, or stop and say.

## Rules that hold everywhere

- **Shared cores, thin front ends.** Booking and availability have one implementation
  (`computeSlots`, `createBookingForSlug`, `planBooking`). The web pages, the API, the
  assistants and the MCP tools all call it. Never re-implement booking logic for one surface.
- **A model or an agent never gains authority by being able to call a tool.** Creating,
  booking, cancelling and every exception to the owner's rules happen on the owner's own tap.
  No model output and no agent call sets `OwnerApproved`, `OutsideRules`, `OverBookings` or
  `OwnerException`.
- **Models see computed availability only.** Never raw calendar contents, other guests or
  other bookings. Models do no time arithmetic.
- **Authorization is checked on the server**, per request, against the signed-in user. Hiding
  something in the UI is not a permission.
- **An assistant feature is not done until its MCP side exists** (tool, instruction line,
  test, parity entry).
- **Translated sentences are composed in Go**, not in JavaScript.
- **Admin UI is shadcn-svelte.** No hand-rolled buttons or modals, never `window.confirm`.

## Commercial and AI-first constraints

Bookmeet today is one workspace per installation: there is no account or tenant column, and
roles (`is_owner`, `is_admin`) are install-wide. Until the owner decides the account model
(`SAAS-READINESS.md` in the vault):

- Do not add tenant, account, plan or billing schema. That is an approval checkpoint.
- Do not add new install-wide singletons (one more global setting, key or bot) without
  saying so: each one is a future migration.
- New data belongs to a user (`user_id`), and new queries filter by it. Add a test that a
  second user cannot read or change it.
- Plan checks, when they exist, go through one entitlement function. Never compare plan names
  in feature code. Feature flags never stand in for authorization.
- For a substantial feature, answer briefly in the pull request: could an agent use this
  without the UI, what would it need, what must the owner approve, how would we measure it.

## Releases

Staging first, always: `packaging/fly/deploy.sh staging`, check it, then `packaging/fly/deploy.sh
production`. **Production needs the owner's explicit yes each time.** The script refuses a
commit that is not on `origin/bookmeet` or that staging is not running. Pull before any
deploy. Bump `VERSION` with every deployed change. Rolling back the app image does not undo a
database migration. Detail: `docs/DEVELOPING.md`, scenario F.

## Where the detail lives

Read the one you need; do not load them all.

| Touching | Read |
|---|---|
| `frontend/` (admin UI, shadcn, visual tests) | `frontend/CLAUDE.md` (loads itself) |
| `book.html`, `manage.html`, `embed.js`, `booking.css`, the book-by-chat assistant | `docs/agent-notes/booking-surfaces.md` |
| Video room, recording, LiveKit tokens | `docs/agent-notes/livekit.md` |
| Locale files, translated strings | `internal/i18n/CLAUDE.md` (loads itself) |
| Mail transports, invites | `internal/mailer/CLAUDE.md` (loads itself) |
| Event type create/update/validation | `docs/agent-notes/event-types.md` |
| Calendar read-back, `external_event_id` | `docs/agent-notes/calendar-readback.md` |
| Personal links, Telegram assistant, guest chat page, exceptions, reports | `docs/agent-notes/personal-assistant.md` |
| MCP assistant tools, cards, `owner_approve` | `docs/agent-notes/personal-assistant-mcp.md` and `docs/agent-capabilities.md` |
| `cmd/lab` | `cmd/lab/CLAUDE.md` (loads itself) |
| `packaging/` | `packaging/CLAUDE.md` (loads itself) |
| How the whole app fits together | `docs/ARCHITECTURE.md` |
| What Bookmeet adds to Calnode, upstream merges | `BOOKMEET.md`, `AGENTS.md` |

When you learn a rule worth keeping, put it in the notes file for that area, not here. This
file stays under 8 KB.

## End of a session

Add a handoff to the vault's `LOG.md` (format in `docs/DEVELOPING.md`, scenario E), update
`TASKS.md`, and record any decision the owner made in `DECISIONS.md`. One writer at a time in
the vault: write once, at the end, and do not edit vault files in the background.
