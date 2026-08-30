# MithyaX for Google Meet (v1 — single participant proof)

Streams the remote participant's video/audio from a live Google Meet call
into MithyaX's existing `/api/v1/sessions/ws` pipeline (the same one
`web/live-session.js` uses) and shows a floating risk badge on the page.

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

- A badge reading "MithyaX ⚪ Analyzing…" appears bottom-right within a
  couple seconds of the remote participant's video appearing.
- Open the service worker console (`chrome://extensions` → this
  extension → "service worker") to see `session_started` / `video_result`
  / `audio_result` / `temporal_result` / `risk_update` messages arriving
  from the gateway.
- The badge updates to 🟢 Real / 🟡 Suspicious / 🔴 AI / High risk with a
  percentage, driven entirely by `risk_update` — the extension never
  scores anything itself.

## How it's wired

- `content.js` polls the DOM for the remote participant's `<video>`/
  `<audio>` elements (identified by an empty `MediaStreamTrack.label` —
  WebRTC-received tracks don't carry a device name the way local
  camera/mic tracks do) and drives `capture.js`.
- `capture.js` samples a JPEG frame every second and buffers ~3s WAV
  chunks from the remote audio, both ported from `web/live-session.js`.
- Capture data crosses a `runtime.connect` port to `background.js`,
  which owns the actual `MithyaXSession` (`websocket.js`) — kept in the
  service worker rather than the content script so it survives Meet's
  SPA navigations.
- `detector.js` folds incoming messages into one risk state; `ui.js`
  renders it into the badge with plain DOM APIs (Meet's Trusted Types
  CSP rejects `innerHTML` from content scripts).

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
  fresh one and retries once. It doesn't yet reconnect a session whose
  WebSocket drops mid-call; that's the Meet-specific lifecycle work
  coming next.

## Known limitations (by design, for this first pass)

- Remote-element detection is a heuristic against an undocumented,
  frequently-changing DOM — expect to revisit it if Meet changes markup.
- Multi-party calls: only the largest on-screen remote tile is analyzed.
- No settings UI yet — gateway URL and extension token are constants in
  `background.js`.
- MV3 service workers can be recycled by Chrome; `content.js`'s
  detection loop will reconnect and restart a session automatically the
  next time it ticks (minting a fresh session credential as part of
  that, same as any other new session).
