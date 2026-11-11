# Bookmeet

A maintained fork of [Calnode](https://github.com/Calnode/calnode), currently based on **v0.13.0**. Read the upstream [README](README.md) for the scheduler itself. The `bookmeet` branch is the source of truth for Bookmeet customizations.

## What's added

- Secure reschedule/cancel links and instructions in Google-sent calendar invitations, including links that survive rescheduling.
- An interactive full-page geometric dot animation. Light mode is the exact color inverse of Dark, with identical geometry, opacity, mouse interaction, and timing. Auto follows the visitor's device preference; a header selector stores a visitor override locally.
- Editable plain-text welcome copy for booking, manage, and directory pages, plus a header note and AI button label.
- Confetti/unicorn booking celebrations, crying-emoji cancellations, previews, Off choices, reduced-motion handling, and finite cleanup.
- Optional removal of public Calnode attribution while preserving legal/consent/language controls.

All operator choices are in **Settings → Branding** and persist in SQLite. The skin is embedded application source, not Tracking injection. Emails remain static.

The **Settings → Branding → Available times** switch controls Morning/Afternoon/Evening groups. Off (the default) shows every time in a single scrollable panel, including rescheduling and embeds.

## In-person meetings

In an event type's Location settings, allow guests to choose an in-person location. Video event types can also include their usual video link for in-person bookings. The guest's address remains the calendar event location. With video enabled, the configured video provider supplies its usual join link.

Manual entry works without configuration. To offer place suggestions, set the server environment variable `BOOKMEET_GOOGLE_PLACES_KEY` to a key restricted to Google Places API (New), with billing and quota configured in Google Cloud. Suggestions send the typed location to Google after a short pause and share a persistent 150-request daily limit across all guests. The Places key stays on the server. For an optional inline map, enable Maps Embed API and set `BOOKMEET_GOOGLE_MAPS_EMBED_KEY` to a separate browser key restricted to the booking site and Maps Embed API. Guests open the map preview on demand.

## Branding assets

The reusable design kit lives in [`branding/`](branding/START-HERE.md): black/white transparent PNG and outlined SVG logos, dark/light vector and PNG backgrounds, CSS, animation code, a local preview, and a Claude CRM brief. The native app files remain the runtime source of truth; the design kit is a portable snapshot. The Home Assistant customization export omits this asset library because the app does not load it.

## Development and checks

Use the Go version required by `go.mod`, Node 22, and pnpm 10.32.1. From `frontend`, run `pnpm install --frozen-lockfile`, `pnpm check`, and `pnpm build`. Then from the repository root:

```sh
node --test internal/handler/assets/bookmeet-skin.test.cjs
go test ./internal/booking ./internal/handler ./internal/mailer ./internal/branding
```

CI also runs the upstream visual and full Go suites and produces a checksummed ARM64 artifact. Inherited upstream image-publishing and CLA write workflows are gated to the upstream repository; this fork does not publish to Calnode's registry or request an upstream CLA for its own changes.

## Updates and Pi packaging

Keep `upstream` pointed to Calnode/calnode. Merge each selected upstream release into an upgrade branch and open a PR to `bookmeet`. Resolve the custom migrations (currently 00072–00079) explicitly if upstream uses those numbers. Test a copy of the database and smoke-test booking, invitation links, rescheduling, cancellation, appearance, and saved copy before deployment.

`packaging/home-assistant/package.py` exports the code patch relative to the pinned base and packages a supplied ARM64 binary with the Home Assistant Dockerfile. The Pi's existing updater can reapply this exported patch and run tests during an upstream update. A patch, migration, or test conflict stops that build. CI artifacts do not automatically deploy to the Pi: `packaging/home-assistant/deploy.sh` deploys the CI-built binary over SSH with checksums, a recovery copy and automatic rollback (see `packaging/home-assistant/README.md`).

Keep the detailed machine-specific deployment runbook in the ignored `local/` folder. The public repository intentionally excludes live configuration, secrets, and booking tokens. Upstream license and notices are retained.

## In-person travel

Shipped: per-event-type extra in-person before/after buffers and drive time (migrations 00078, 00080); a per-user private home address (00079, Settings -> Profile); and Google-estimated drive time.

- **Basic buffers apply to every meeting; the in-person extras are additive.** Reserved minutes are stored on the booking (`reserved_before/after_minutes`) and honoured by overlap checks, slots, reschedule and reassign. `GET /slots?in_person=1&location=<address>` returns the in-person set; the booking page re-requests it when "In person" is ticked or the address changes and shows "Factoring in drive time...". If the already-picked time no longer fits, the guest is sent back to choose.
- **Drive time modes (`event_types.travel_mode`):** `fixed` holds `fixed_drive_minutes` before every in-person meeting. `google` asks the Routes API (`internal/handler/travel.go`): each of the host's other meetings is widened by the drive between its address (or the host's home address when it has none) and the guest's address; at booking the same lookups set the reserved time (previous in-person meeting or home -> guest address, and guest address -> next in-person meeting). Until an address is known, `fixed_drive_minutes` stands in.
- **Fallbacks and limits:** any lookup failure (no key, daily cap of 400 lookups, Google error, unroutable address) uses `fixed_drive_minutes`; one failure stops further Google calls for that request; answers are cached 12 h. The home address and other meetings' addresses are only sent to Google as route endpoints, never returned to guests.
- **Key:** `BOOKMEET_GOOGLE_ROUTES_KEY`, else the existing `BOOKMEET_GOOGLE_PLACES_KEY` (set in the app's env file). The Routes API must be enabled on that Google Cloud project. `BOOKMEET_ROUTES_URL` redirects the endpoint for tests only.
- **Known gaps:** only Bookmeet bookings supply neighbouring meeting addresses; events that exist only on the host's Google Calendar are not read for locations. Rescheduling re-offers times with Google drive times, but the final overlap check uses the allowance the booking already holds. The embed widget has no in-person option.

## Build version on public pages

The public skin shows the build version in small type at the foot of every public page. The number lives in `VERSION` at the repository root as `1.N` (1.16 was the last Raspberry Pi build, `0.13.0+bookmeet.16`) and is stamped into the binary at deploy time. Bump it for every release so the number changes with each push that gets deployed. The upstream Calnode release the fork is based on is tracked separately in `packaging/home-assistant/upstream-base.txt`.

## Hosting

Bookmeet runs on Fly.io as one always-on machine with its SQLite database on a volume and continuous Litestream backup to object storage. `packaging/fly/fly.toml` is the production configuration and `packaging/fly/fly.staging.toml` the staging one (app `bookmeet-staging`, https://staging.bookmeet.date, test data only, machine stops when idle). `packaging/fly/deploy.sh staging` then `packaging/fly/deploy.sh production` deploys the pushed commit and checks that the app reports it; production only takes a commit that staging is running. Secrets are Fly secrets, never files in this repository. Logo, banner and avatar files live on the volume beside the database and are not part of the Litestream backup; Fly's volume snapshots cover them.

## Week start on public pages

The host's "First day of week" (Settings -> Profile) sets the first column of the calendar on the booking page, the manage page and the embed widget. It used to apply only inside the admin; public calendars were always Monday-first.
