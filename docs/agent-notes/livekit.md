# Built-in video meetings (LiveKit)

Moved here unchanged from the root `CLAUDE.md`. The root file lists when to read this.

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
