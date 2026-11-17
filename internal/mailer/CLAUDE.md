# Email — agent notes

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

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
