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

- A badge reading "MithyaX ⚪ Connecting…" appears over each remote
  participant's tile within a couple seconds of their video appearing —
  one badge per participant, up to `MAX_CONCURRENT_PARTICIPANTS` (4 by
  default; see `content.js`).
- Open the service worker console (`chrome://extensions` → this
  extension → "service worker") to see `session_started` / `video_result`
  / `audio_result` / `temporal_result` / `risk_update` messages arriving
  from the gateway — one independent stream of these per participant.
- The badge moves to "⚪ Analyzing…" once a session is actually live, then
  to a plain-language verdict — 🟢 Likely Authentic / 🟡 Suspicious / 🔴
  Likely AI-Generated — driven entirely by that participant's own
  `risk_update`. Clicking "Why?" on a verdict expands a short explanation
  and confidence percentage; see "How the badge communicates" below for
  the full state model and why raw model scores are never shown. One
  participant's verdict never affects another's badge.

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

## How the badge communicates (Phase 8.8)

The goal isn't more detection infrastructure — it's making sure someone
who has never seen MithyaX before can look at a badge and immediately
answer: what is it doing, what does this result mean, should they be
concerned, and is it currently working at all. Concretely, that meant
never showing a raw model number and never letting a connection problem
look like a risk verdict.

- **State model** (`detector.js` produces `"analyzing"`/`"verdict"` from
  the risk engine's own messages; `content.js` constructs the rest —
  see each file's own doc): `Connecting…` (before a session exists yet)
  → `Analyzing…` (session live, no confident verdict yet — this is also
  where a `risk_update` with verdict `UNKNOWN`, i.e. no usable signal at
  all, lands; it reads to a user exactly like "still gathering data",
  not a fourth kind of result) → 🟢 **Likely Authentic** / 🟡
  **Suspicious** / 🔴 **Likely AI-Generated**. Independently,
  `Reconnecting… (attempt N)`, `Analysis unavailable`, and
  `Participant left` cover the operational/connection states — these are
  facts about the connection, not the risk engine, and are deliberately
  styled distinctly (`⚠️`, its own `data-tier="unavailable"`, a muted
  amber never confusable with the red "likely fake" styling) so a
  connection problem can never be mistaken for an actual detection
  result.
- **Never a raw score.** `video_score: 0.731` never reaches the page.
  `detector.js` derives a `confidence` percentage instead — not the same
  number as the risk engine's raw [0,1] fake-likelihood score, but its
  *meaning*: how confident a user should be in the verdict they're
  looking at. A low fake-score is *why* something is called authentic,
  so confidence rises as the score falls toward 0 for that verdict; a
  high score is why something is called fake, so confidence rises as the
  score climbs toward 1 there. `reasons` are already human sentences
  from the gateway's own risk engine (e.g. "Video signal indicates
  likely synthetic or manipulated content") — never bare numbers — and
  the raw per-modality scores stay on the state object only for
  developer console debugging; `ui.js` never renders them.
- **Details are opt-in, not upfront.** The compact pill badge is always
  what's visible; clicking "Why?" (only shown for an actual verdict —
  there's nothing to explain about "Connecting…") expands a small panel
  with the plain-language description, the confidence percentage, and
  the reasons list. A hover tooltip mirrors the same text as a zero-click
  fallback. `pointer-events: none` stays on the badge as a whole (so it
  never blocks clicking through to Meet's own controls underneath) —
  only the toggle button and the details panel it opens turn
  `pointer-events: auto` back on for themselves.
- A terminal, human-facing message (`Participant left`,
  `Analysis unavailable`) stays visible for `PARTICIPANT_LEFT_DISPLAY_MS`
  (2.5s, in `content.js`) before the badge is actually removed — without
  that delay, `removeBadge` would erase the very text `updateBadge` just
  set in the same synchronous step, and a person would never actually
  see it. If the participant's video reappears during that window (a
  brief blip, not a real departure), the pending removal is cancelled.
- Verified directly against the real gateway (Phase 8.8's own
  end-to-end check): a genuine `risk_update` correctly produced
  `{headline: "Likely Authentic", description: "...", confidence: 67}`
  from a real temporal score of 0.333 — confirming the confidence
  derivation is correct against real data, not just synthetic test
  inputs.

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

## How it handles Meet's own navigation (Phase 8.6)

Meet is a single-page app: leaving one call and joining another can
happen entirely through client-side routing, with no page reload — so
no fresh `content.js` injection ever comes along to reset this file's
state the way a real navigation would.

- **A fast, explicit "the meeting changed" signal, on top of the
  existing passive one.** Meet's meeting code lives in the URL path
  (its well-known public link format, `meet.google.com/xxx-yyyy-zzz`),
  so `tick()` compares just `location.pathname` (deliberately not the
  full URL — Meet may change query string/hash for in-call UI state
  that isn't a different meeting, which must not cause a false-positive
  teardown of participants who never left) against what it last saw.
  When it changes, every currently-tracked participant is torn down
  immediately — badge removed, port stopped — rather than waiting on
  the several-second passive "video element disappeared" detection
  (which still exists and still works as a fallback; this is a faster,
  more specific complement to it, not a replacement).
- **A real race this surfaced, fixed in `background.js`.** Because
  `connectWithSession` is async, a "stop" (from the transition above, or
  from a participant's ordinary grace-period timeout) can arrive while a
  connect or reconnect attempt for that same participant is already in
  flight — at that exact moment there's no live `session` object yet
  for "stop" to end. Without a fix, that in-flight attempt would later
  resolve, quietly assign itself as the live session, and keep running:
  an orphaned WebSocket and a gateway session nobody asked for, for a
  meeting that's already gone. `background.js` now tags every
  connect/reconnect attempt with a generation number that any stop,
  disconnect, or fresh start bumps; an attempt that resolves after being
  superseded is discarded and immediately ended rather than ever being
  assigned live. Verified directly against the real gateway with an
  artificially delayed connect to force the exact race: the socket does
  open, and is then closed again within the same tick — content.js
  never sees so much as a `state` message for it.

## Multi-participant stress verification (Phase 8.7)

Rather than testing `content.js` and `background.js` separately (as
8.4-8.6 did), this phase wired the real, unmodified files together
through a genuine two-way port pair — the same integration Chrome
itself provides — against the real running gateway, and drove one long
scenario with four concurrent participants (A, B, C, D): all four
joining up to the concurrency cap, A leaving, B's tile element being
replaced, C briefly disappearing and returning, A rejoining as a
genuinely new participant, D and B's WebSockets being force-closed
*simultaneously* to stress independent concurrent reconnects, tile
reordering, and three rapid full join/leave/rejoin cycles for a fifth
participant. 32 checks passed: stable identity across reordering, no
cross-talk between any two participants' sessions or audio, exactly one
session per join with no orphans across rapid cycling, isolated
concurrent reconnects, and zero leaked sockets at the end. No new bugs
turned up this time — the 8.4/8.5/8.6 fixes already covered this.

## Security/reliability hardening pass (Phase 8.9)

A small, focused audit against a specific checklist (extension token
handling, credential leakage, console log content, message validation,
timer/port/WebSocket cleanup, duplicate sessions) rather than another
architectural phase. Two real, concrete findings — not padding:

- **Console logging could leak the session credential.**
  `websocket.js`'s WebSocket `onerror` handler used to forward the raw
  native `Event` straight to `background.js`'s `console.error(...)`. A
  WebSocket `Event`'s `.target` is the socket itself, and
  `this.ws.url` is the `sessions/ws` URL — which carries the session
  credential as a query parameter (see "How it authenticates"). Nothing
  printed it directly, but a developer expanding that logged object in
  DevTools could have read the credential straight off
  `event.target.url`. Now `onerror` only ever constructs and forwards a
  plain `Error("session websocket error")` — a WebSocket error event
  carries no diagnostic payload of its own by browser design, so nothing
  informative was lost. Verified directly: fired a fake native-shaped
  error event whose `.target` exposed a real credential-bearing URL, and
  confirmed neither the object `onError` receives nor its stringified
  form contain the credential.
- **Two of `content.js`'s three top-level timers were never actually
  stoppable.** `tickTimer` and `frameTimer`'s `setInterval` return
  values were discarded at the call site — only `positionTimer` was
  captured. `handleFatal` (extension context invalidated — e.g. the
  extension was reloaded/updated while a tab was still open) correctly
  stopped polling logically (`killed` short-circuits both `tick()` and
  `sampleFrame()`), but the underlying intervals themselves kept firing,
  harmlessly no-opping, for the rest of that tab's lifetime instead of
  actually being cleared. Fixed by capturing and clearing all three.
  Verified: after `handleFatal()` fires, advancing far past when either
  timer would have fired again confirms neither does.
- Also added minimal shape validation on both ends of the
  `content.js` ↔ `background.js` port (`message` must be an object with
  a string `type`; `frame`/`audio_chunk` payloads must be strings) —
  this isn't a defense against a hostile sender (a Meet page script has
  no way to get a reference to this port at all; content scripts run in
  an isolated JavaScript world Chrome enforces, which is also the
  underlying reason `window.__mithyax` in `capture.js`/`ui.js` is never
  reachable from the Meet page's own `<script>` tags) — it's robustness
  against a malformed message reaching a `switch` and throwing out of an
  event listener. Verified with a battery of malformed inputs (`null`,
  wrong types, unexpected shapes) against both listeners directly.
- Everything else on the checklist — port disconnect cleanup, WebSocket
  cleanup, duplicate-session prevention, gateway-unavailable and
  extension-reload behavior, multi-participant rapid lifecycle events —
  was already covered by 8.4-8.7's work and re-confirmed rather than
  changed; see those phases' own sections above for what was actually
  built and how it was verified.
- **Critical security check, confirmed by direct inspection**:
  `GATEWAY_EXTENSION_TOKEN` and the short-lived session credential it's
  exchanged for appear nowhere outside `background.js`/`websocket.js` —
  grepped across every file. `content.js`'s only mention of either term
  is a code comment.

## ⚠️ Outstanding validation items for the real Meet test

These are not bugs, and not things to "fix" based on assumption — they
are heuristics/strategies whose correctness depends on Meet's actual
DOM, which nothing in this codebase can observe without a real call.
Simulating a plausible DOM shape and getting a passing test (as 8.3, 8.4,
and 8.7 all did) is evidence the *logic* is sound given that shape — it
is not evidence the shape itself is what Meet actually produces. Treat
both items below as open until a real multi-participant Meet call
confirms or refutes them; do not silently reinterpret "passed in
simulation" as "verified."

- **Audio-to-participant pairing under genuinely decoupled audio/video
  streams is unverified.** `refreshAudioPairing` (`content.js`) has a
  confident primary strategy for the common case — one combined
  MediaStream carrying both a participant's video and audio track,
  checked first, unambiguous regardless of participant count — and every
  simulation so far (8.4's and 8.7's stress test alike) has exercised
  exactly that case, because that's the only shape a simulated DOM can
  assert without guessing at Meet's real internals. The fallback for a
  *decoupled* `<audio>` element only fires when there's exactly one
  participant needing audio and exactly one unclaimed stream — with two
  or more of either, it deliberately leaves them without audio rather
  than guessing (see "How it detects remote participants" above). Nobody
  has yet confirmed whether real Meet ever produces the decoupled shape
  at all with 2+ simultaneous remote participants, or how it's
  structured in the DOM if it does. **Required test**: a real call with
  2+ people talking, confirming each participant's `audio_result`
  reacts to *their own* voice — not the assumption that the current
  code is already correct.
- Remote-element detection is a heuristic against an undocumented,
  frequently-changing DOM — expect to revisit it if Meet changes markup
  (see "How it detects remote participants" above).

## Known limitations (by design, for this first pass)

- Capped at `MAX_CONCURRENT_PARTICIPANTS` (4) simultaneous participants
  per tab — a deliberate limit on this tab's own resource usage, not
  the gateway's.
- **The meeting-transition detection (Phase 8.6) trusts `location.pathname`
  as Meet's meeting-code boundary** — reasonable given it's Meet's own
  public link format, but not re-verified against every in-call URL
  mutation Meet might make (e.g. some in-call overlay changing the path
  itself rather than just query/hash, which isn't expected but hasn't
  been ruled out live). If that ever turns out to happen, the practical
  effect would be an unnecessary participant teardown/restart, not a
  correctness or leak issue — the passive detection and the reconnect
  generation guard are independent safety nets underneath it either way.
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
