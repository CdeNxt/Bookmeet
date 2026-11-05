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

## Development and checks

Use the Go version required by `go.mod`, Node 22, and pnpm 10.32.1. From `frontend`, run `pnpm install --frozen-lockfile`, `pnpm check`, and `pnpm build`. Then from the repository root:

```sh
node --test internal/handler/assets/bookmeet-skin.test.cjs
go test ./internal/booking ./internal/handler ./internal/mailer ./internal/branding
```

CI also runs the upstream visual and full Go suites and produces a checksummed ARM64 artifact. Inherited upstream image-publishing and CLA write workflows are gated to the upstream repository; this fork does not publish to Calnode's registry or request an upstream CLA for its own changes.

## Updates and Pi packaging

Keep `upstream` pointed to Calnode/calnode. Merge each selected upstream release into an upgrade branch and open a PR to `bookmeet`. Resolve the custom migrations (currently 00072–00074) explicitly if upstream uses those numbers. Test a copy of the database and smoke-test booking, invitation links, rescheduling, cancellation, appearance, and saved copy before deployment.

`packaging/home-assistant/package.py` exports the code patch relative to the pinned base and packages a supplied ARM64 binary with the Home Assistant Dockerfile. The Pi's existing updater can reapply this exported patch and run tests during an upstream update. A patch, migration, or test conflict stops that build. CI artifacts do not automatically deploy to the Pi.

Keep the detailed machine-specific deployment runbook in the ignored `local/` folder. The public repository intentionally excludes live configuration, secrets, and booking tokens. Upstream license and notices are retained.
