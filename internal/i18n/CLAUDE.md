# Languages (i18n) — agent notes

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

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
