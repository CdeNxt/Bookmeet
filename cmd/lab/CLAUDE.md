# The assistant lab — agent notes

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

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
