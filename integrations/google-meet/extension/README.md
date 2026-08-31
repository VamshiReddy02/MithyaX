# MithyaX for Google Meet

Streams every remote participant's video/audio from a live Google Meet
call into MithyaX's existing `/api/v1/sessions/ws` pipeline (the same one
`web/live-session.js` uses) and shows one floating risk badge per
participant on the page — each with its own independent session,
WebSocket, and risk state (Phase 8.4).

## Load it

1. Start the gateway (`localhost:8080` by default — edit
   `GATEWAY_HTTP_URL` in `background.js` if yours runs elsewhere, and add
   its origin to `host_permissions` in `manifest.json`).
2. Edit `GATEWAY_EXTENSION_TOKEN` in `background.js` to match the
   gateway's own `GATEWAY_EXTENSION_TOKEN` (see
   `deployments/docker/.env`) — **not** `GATEWAY_AUTH_TOKEN` or
   `GATEWAY_ADMIN_AUTH_TOKEN`. See "How it authenticates" below for why
   this one, specifically, is safe to ship inside an extension.
3. `chrome://extensions` → enable Developer Mode → **Load unpacked** →
   select this `extension/` directory.
4. Join a Meet call **with at least one other participant** (a remote
   video/audio track is what the extension looks for — there's nothing
   to analyze in a solo call).

## What "working" looks like

- A badge reading "MithyaX ⚪ Analyzing…" appears over each remote
  participant's tile within a couple seconds of their video appearing —
  one badge per participant, up to `MAX_CONCURRENT_PARTICIPANTS` (4 by
  default; see `content.js`).
- Open the service worker console (`chrome://extensions` → this
  extension → "service worker") to see `session_started` / `video_result`
  / `audio_result` / `temporal_result` / `risk_update` messages arriving
  from the gateway — one independent stream of these per participant.
- Each badge updates to 🟢 Real / 🟡 Suspicious / 🔴 AI / High risk with a
  percentage, driven entirely by that participant's own `risk_update` —
  the extension never scores anything itself, and one participant's
  verdict never affects another's badge.

## How it's wired

- `content.js` polls the DOM for every remote participant's `<video>`/
  `<audio>` elements (identified by a `MediaStreamTrack.label` starting
  with `"remote"` — Meet's own convention for WebRTC-received tracks,
  as opposed to a local camera/mic's real device name; see "How it
  detects remote participants" below) and drives `capture.js` once per
  participant.
- `capture.js` samples a JPEG frame every second and buffers ~3s WAV
  chunks from a participant's remote audio, both ported from
  `web/live-session.js` — one `RemoteAudioCapture` instance per
  participant, entirely independent of any other's.
- Each participant's capture data crosses its own `runtime.connect` port
  to `background.js`, which owns that participant's own `MithyaXSession`
  (`websocket.js`) — kept in the service worker rather than the content
  script so it survives Meet's SPA navigations.
  `background.js` needed no changes for multiple participants: its
  session/state/error handling was already scoped per port inside its
  `onConnect` listener, not a module-level singleton, so N simultaneous
  ports from `content.js` already give each one a fully independent
  session for free.
- `detector.js` folds one participant's incoming messages into one risk
  state (background.js keeps one `detector.js` state per port, i.e. per
  participant); `ui.js` renders each into its own badge, keyed by
  participant, with plain DOM APIs (Meet's Trusted Types CSP rejects
  `innerHTML` from content scripts).

## How it detects remote participants

`content.js` polls the live DOM every 2s (`tick()`) rather than reacting
to DOM mutation events — Meet's own internal markup for this is
undocumented and changes without notice, so re-querying fresh each tick
is more robust than caching a node reference that might silently go
stale. Every currently-valid remote video tile is tracked independently
in a `Map`, keyed by that tile's video `MediaStreamTrack.id` — a
spec-guaranteed stable id for that track's lifetime, unaffected by which
DOM element currently renders it. A few specific behaviors worth
knowing, since they're easy to get wrong for a page whose structure the
extension doesn't control:

- **Own camera/mic exclusion is allow-list, not block-list**: a
  `<video>`/`<audio>` element is only ever treated as a remote
  participant if its track's `label` starts with `"remote"` — Meet's own
  convention for WebRTC-received tracks, confirmed against a live call.
  Local camera/mic tracks keep their real device name and simply never
  match; nothing about local devices is special-cased or excluded by
  name. This is deliberately the safer shape (a track must positively
  prove it's remote, rather than merely fail to look local) for the one
  invariant that must never break — the user's own camera/microphone
  must never reach the gateway. `sampleFrame` re-checks this on every
  single frame, per participant, as a fail-safe against a future
  regression — not just once at selection time.
- **A missed detection doesn't instantly end a participant's session**:
  losing track of a participant's `<video>` for one poll (a tile being
  replaced, a camera-off blip, Meet's SPA reshuffling the call view)
  used to tear their whole session down immediately — a new session
  means a new WebSocket and a reset risk state, which is disruptive for
  something that often resolves itself within a couple of seconds.
  `PARTICIPANT_LEFT_GRACE_TICKS` in `content.js` requires the miss to
  persist for several consecutive polls (~6s) before treating it as a
  genuine "this participant left" — and only that one participant's
  session ends; everyone else tracked is unaffected.
- **Each participant's remote `<audio>` is re-checked every poll, not
  just once**: if Meet replaces a participant's audio element or
  renegotiates their stream mid-call, `content.js` notices the stream
  identity changed and swaps in a fresh capture for that participant
  rather than continuing to process a stale one.
- **Audio-to-participant pairing** (see `refreshAudioPairing` in
  `content.js`): the common case is one combined MediaStream per
  participant (a video track and an audio track on the same stream,
  played through one `<video>` element) — checked first, and unambiguous
  regardless of how many participants there are. When a participant's
  own video stream doesn't carry audio, and there's a decoupled `<audio>`
  element elsewhere, it's only paired up when there's exactly one
  participant needing audio and exactly one unclaimed stream — with two
  or more of either, pairing is genuinely ambiguous from the DOM alone,
  and nothing is guessed (that participant just goes without audio until
  the ambiguity resolves) rather than risking one participant's voice
  landing in another's session. **This has been verified in simulation,
  not against a real multi-party call** — see "Known limitations".
- **Multiple remote tiles**: every candidate is detected and gets its
  own independent session, up to `MAX_CONCURRENT_PARTICIPANTS` (4 by
  default) — a cap on this browser tab's own resource usage (each
  participant is a full WebSocket + AudioContext + capture loop), not on
  the gateway, which enforces its own much larger
  `RealtimeMaxSessions` server-side. Already-tracked participants are
  never evicted to make room for a new one; `console.debug` logs when a
  newly-detected tile has to wait for a slot.

These behaviors are covered by a standalone simulation harness (a fake
DOM + fake timers driving the real, unmodified `content.js` in Node)
exercised while auditing/extending this file, rather than only by
reasoning about the code — but that's still not a substitute for a real
Meet call. Re-verify `isRemoteTrack`'s label match, and especially the
audio-pairing strategy above, against DevTools on a live call with **2+
simultaneous remote participants** before relying on this for anything
beyond a proof of concept.

## How it authenticates

The extension never carries the gateway's long-lived `GATEWAY_AUTH_TOKEN`
or `GATEWAY_ADMIN_AUTH_TOKEN` — either would grant full API access if the
extension's (unpacked, fully inspectable) source ever leaked. Instead:

```
content.js  ──"start"──►  background.js  ──long-lived extension token──►  POST /api/v1/auth/session
                                                                                    │
                                                                          short-lived credential
                                                                                    ▼
                                                                          POST /api/v1/sessions,
                                                                          then the sessions/ws WebSocket
```

- `GATEWAY_EXTENSION_TOKEN` (`background.js`) authorizes exactly one
  call — `POST /api/v1/auth/session` — and nothing else in the gateway's
  API (see `internal/auth.ExtensionMiddleware` on the gateway side). A
  leaked copy of it lets someone mint session credentials; it can't
  reach `/analyze`, `/analysis`, or any admin route.
- `background.js` exchanges it for a short-lived session credential
  (`getSessionCredential`), caches it in memory only (never
  `chrome.storage`) until shortly before it expires, and hands it to
  `websocket.js`'s `MithyaXSession.connect()` — which is the only place
  that actually uses it, as the `Authorization` header on
  `POST /api/v1/sessions` and the `credential` query parameter on the
  `sessions/ws` WebSocket (a browser `WebSocket` can't attach a custom
  header to its handshake, the same reason `session_id` is already a
  query parameter there).
- `content.js`, `capture.js`, `detector.js`, and `ui.js` — everything
  that runs inside the Meet page itself — never see either token. They
  only ever exchange `{type: "start"|"frame"|"audio_chunk"|"stop"}` and
  `{type: "state", ...}` messages with `background.js` over the
  `runtime.connect` port.
- If the gateway rejects a cached credential outright (401) —
  it expired, or was otherwise invalidated — `background.js` mints a
  fresh one and retries once. A session whose WebSocket drops for any
  other reason is handled by the reconnect logic below, which reuses
  this exact same credential machinery on every attempt.

## How it recovers from disconnects

A participant's WebSocket can drop for reasons that have nothing to do
with them — a network blip, the gateway restarting — and `background.js`
reconnects automatically rather than leaving `content.js` to notice and
rebuild everything from scratch (Phase 8.5):

- **Reconnect means a new session, not resumed history.** There's no
  server-side concept of "resume session X" — the gateway finalizes and
  deletes a session the moment its WebSocket closes, whatever the
  reason (see `internal/handlers/sessionswebsocket.go`). So a reconnect
  goes through the exact same `POST /api/v1/auth/session` →
  `POST /api/v1/sessions` → WebSocket flow any new participant does,
  with a fresh session id — analysis resumes, but that one gap's risk
  history doesn't carry over. `background.js` resets its local detector
  state at the same point, so the badge doesn't show stale numbers from
  the now-dead session while this happens.
- **Exponential backoff with jitter, and a hard cap.** Delay doubles
  each attempt (1s, 2s, 4s, ... capped at 30s), with up to ±20% random
  jitter so several participants' retries don't all hit the gateway at
  the same instant. `RECONNECT_MAX_ATTEMPTS` (8, in `background.js`) —
  roughly two minutes of total retrying — bounds this; past it,
  `background.js` gives up for good and reports the same `{type:"error"}`
  content.js already handles by tearing that one participant down. That
  gives a real gateway restart a fair chance without retrying forever.
- **Only an unexpected drop reconnects.** A deliberate stop
  (`content.js` sending `{type:"stop"}`, or the port itself
  disconnecting) is tracked separately and never triggers a reconnect —
  only the WebSocket's own `close` event firing when nothing asked for
  it does.
- **Fully isolated per participant.** This logic lives entirely inside
  the same per-port closure 8.4 already uses for session isolation — no
  new mechanism was needed for "participant A's reconnect must not
  affect B." Verified directly (not simulated) against the real gateway:
  force-closing one simulated participant's WebSocket, and a real
  `docker restart` of the gateway container mid-session, both reconnect
  successfully while an unrelated second participant's own connection
  and message stream are completely undisturbed throughout.
- While reconnecting, `content.js` shows "Reconnecting… (attempt N)" on
  that participant's badge (a new `{type:"reconnecting"}` message) but
  does **not** tear the participant down — their tracked entry, and
  `tick()`'s own independent video-detection, keep running the whole
  time, so a successful reconnect just resumes rather than making the
  participant disappear and reappear as a new tile.

## Known limitations (by design, for this first pass)

- Remote-element detection is a heuristic against an undocumented,
  frequently-changing DOM — expect to revisit it if Meet changes markup
  (see "How it detects remote participants" above).
- **Audio-to-participant pairing has only been verified in simulation,
  not against a real Meet call with multiple simultaneous remote
  participants** — this is the one part of Phase 8.4 that needs live
  confirmation before being relied on beyond a proof of concept. If real
  Meet DOM turns out to decouple audio/video per tile even with several
  participants present, the "exactly one unclaimed audio stream" fallback
  in `refreshAudioPairing` won't be able to attribute audio for more than
  one of them at a time (it won't guess wrong — it'll just leave those
  participants without audio) until a real DOM-proximity strategy is
  built from what an actual call's structure shows.
- Capped at `MAX_CONCURRENT_PARTICIPANTS` (4) simultaneous participants
  per tab — a deliberate limit on this tab's own resource usage, not
  the gateway's.
- No settings UI yet — gateway URL, extension token, and the
  concurrency cap are all constants in `background.js`/`content.js`.
- MV3 service workers can be recycled by Chrome; when that happens
  mid-session, `content.js`'s detection loop will reconnect and restart
  each affected participant's session automatically the next time it
  ticks (minting a fresh session credential as part of that, same as
  any other new session) — other participants' sessions, on independent
  ports, are unaffected.
- Reconnecting after a dropped WebSocket (see "How it recovers from
  disconnects" above) always starts a **new** session — there's no
  server-side session continuity, so a reconnect's risk history starts
  over rather than picking up exactly where the old session left off.
  Building true continuity would mean gateway-side changes (a grace
  period before finalizing a session, accepting a reconnect to an
  *existing* session id) that this phase deliberately didn't take on.
- After `RECONNECT_MAX_ATTEMPTS` (8, ~2 minutes of backoff) fails to
  reconnect a participant, that participant's session is torn down for
  good — `content.js`'s own detection loop is what would notice them
  again and start fresh, same as if they'd genuinely left and rejoined.
