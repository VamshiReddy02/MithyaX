# MithyaX for Google Meet (v1 — single participant proof)

Streams the remote participant's video/audio from a live Google Meet call
into MithyaX's existing `/api/v1/sessions/ws` pipeline (the same one
`web/live-session.js` uses) and shows a floating risk badge on the page.

## Load it

1. Start the gateway (`localhost:8080` by default — edit
   `GATEWAY_HTTP_URL` in `background.js` if yours runs elsewhere, and add
   its origin to `host_permissions` in `manifest.json`).
2. `chrome://extensions` → enable Developer Mode → **Load unpacked** →
   select this `extension/` directory.
3. Join a Meet call **with at least one other participant** (a remote
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

## Known limitations (by design, for this first pass)

- Remote-element detection is a heuristic against an undocumented,
  frequently-changing DOM — expect to revisit it if Meet changes markup.
- Multi-party calls: only the largest on-screen remote tile is analyzed.
- No settings UI yet — gateway URL is a constant in `background.js`.
- MV3 service workers can be recycled by Chrome; `content.js`'s
  detection loop will reconnect and restart a session automatically the
  next time it ticks.
