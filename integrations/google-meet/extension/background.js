// background.js — the extension's service worker. Owns one MithyaXSession
// per Meet tab (keyed by the runtime.Port that tab's content.js opens)
// and the running detector state for it, and relays between the two:
// capture data in from content.js, labeled risk state back out to it.
//
// This is also the whole of the extension's security boundary (Phase
// 8.1/8.2): the extension token read via getConfig() below is read only
// in this file, never forwarded to content.js/detector.js/ui.js or any
// code that runs in the Meet page itself — those only ever exchange
// plain "start"/"frame"/"audio_chunk"/"stop"/"state" messages over a
// runtime.connect port, none of which carries a credential of any kind.
// This file exchanges that token for a short-lived session credential
// (getSessionCredential) and is the only thing that ever uses either
// one — see websocket.js for how the credential then flows into
// POST /api/v1/sessions and the sessions/ws WebSocket.
//
// Phase 8.10: gateway URL + token used to be hardcoded constants edited
// by hand; a real pilot tester can't do that, so both now live in
// chrome.storage.local, set via options.html/options.js (opened from
// the toolbar icon — see the chrome.action listener below) and read
// fresh by getConfig() on every "start". Not cached at module scope:
// this service worker can be recycled by Chrome at any time anyway, and
// a value the user just changed in the options page should take effect
// on the very next connection attempt, not after a restart.
import { MithyaXSession, SessionCreateError } from "./websocket.js";
import { createDetectorState, applyMessage } from "./detector.js";

// getConfig returns null if setup hasn't happened yet (fresh install,
// options page never opened/saved) — every caller below treats that as
// a distinct, expected state, not an error.
async function getConfig() {
  const { gatewayUrl, extensionToken } = await chrome.storage.local.get(["gatewayUrl", "extensionToken"]);
  if (!gatewayUrl || !extensionToken) return null;
  return { gatewayUrl, extensionToken };
}

// A first-run affordance: this extension has no popup, so clicking the
// toolbar icon goes straight to the one place setup actually happens.
chrome.action.onClicked.addListener(() => {
  chrome.runtime.openOptionsPage();
});

// How much earlier than its real expiry a cached session credential is
// treated as already expired — guards against starting a session with
// one that's about to expire mid-request rather than only reacting
// after the gateway has already rejected it.
const CREDENTIAL_EXPIRY_SAFETY_MARGIN_MS = 30_000;

// Reconnect backoff (Phase 8.5): a session dropping — network blip,
// gateway restart — reconnects automatically rather than leaving
// content.js to notice and rebuild everything from scratch. Delay
// doubles each attempt starting from RECONNECT_BASE_DELAY_MS, capped at
// RECONNECT_MAX_DELAY_MS, with up to ±20% jitter so several
// participants' retries (each an independent state machine — see
// chrome.runtime.onConnect below) don't all hit the gateway in lockstep.
// RECONNECT_MAX_ATTEMPTS bounds total retrying to roughly two minutes
// (1+2+4+8+16+30+30+30s) before giving up for good — long enough to
// ride out a real gateway restart, not indefinite.
const RECONNECT_BASE_DELAY_MS = 1_000;
const RECONNECT_MAX_DELAY_MS = 30_000;
const RECONNECT_MAX_ATTEMPTS = 8;
const RECONNECT_JITTER_RATIO = 0.2;

function reconnectDelayMs(attempt) {
  const base = Math.min(RECONNECT_BASE_DELAY_MS * 2 ** (attempt - 1), RECONNECT_MAX_DELAY_MS);
  const jitter = base * RECONNECT_JITTER_RATIO * (Math.random() * 2 - 1);
  return Math.max(0, Math.round(base + jitter));
}

// The current session credential, if any — in-memory only (never
// chrome.storage): it's meant to be short-lived and cheaply reissued,
// and this service worker can be recycled by Chrome at any time anyway,
// which already forces a fresh one to be minted on the next use.
let cachedCredential = null; // { token, expiresAt }

// getSessionCredential returns a currently-valid session credential for
// the given config, exchanging its extensionToken for a new one via
// POST /api/v1/auth/session if none is cached or the cached one is at
// (or near) expiry. forceRefresh skips the cache entirely — used after
// the gateway itself has rejected a credential (see connectSession's
// 401 retry), which is a stronger signal than this function's own clock
// that it's no longer good.
async function getSessionCredential(config, forceRefresh = false) {
  if (!forceRefresh && cachedCredential && cachedCredential.expiresAt - CREDENTIAL_EXPIRY_SAFETY_MARGIN_MS > Date.now()) {
    return cachedCredential.token;
  }

  const resp = await fetch(`${config.gatewayUrl}/api/v1/auth/session`, {
    method: "POST",
    headers: { Authorization: `Bearer ${config.extensionToken}` },
  });
  if (!resp.ok) {
    cachedCredential = null;
    throw new Error(`failed to authenticate with gateway: HTTP ${resp.status}`);
  }

  const { credential, expires_at } = await resp.json();
  cachedCredential = { token: credential, expiresAt: Date.parse(expires_at) };
  return cachedCredential.token;
}

// connectSession gets a valid session credential and opens a
// MithyaXSession with it, retrying once with a freshly-minted
// credential if the gateway rejects the cached one outright (401) —
// covers the credential expiring, or otherwise no longer being valid,
// between getSessionCredential's own clock-based check and the gateway
// actually seeing the request.
async function connectSession(config, callbacks) {
  const session = new MithyaXSession(config.gatewayUrl, callbacks);
  let credential = await getSessionCredential(config);
  try {
    await session.connect(credential);
  } catch (err) {
    if (!(err instanceof SessionCreateError) || err.status !== 401) throw err;
    credential = await getSessionCredential(config, true);
    await session.connect(credential);
  }
  return session;
}

chrome.runtime.onConnect.addListener((port) => {
  if (port.name !== "mithyax") return;

  let state = createDetectorState();
  let session = null;
  let disconnected = false;

  // Phase 8.5 reconnect state — entirely local to this one port, i.e.
  // this one participant (8.4's per-port model already isolates
  // participants from each other; reconnect logic just lives inside
  // that same isolation, nothing new to build there). intentionalStop
  // is what tells the websocket's onclose callback (below) apart a
  // deliberate stop/teardown from an unexpected drop — only the latter
  // should ever schedule a reconnect.
  let intentionalStop = false;
  let reconnectAttempts = 0;
  let reconnectTimer = null;

  // generation guards against a real race (Phase 8.6, found while
  // auditing Meet's SPA navigation behavior): connectWithSession is
  // async, so a "stop" (or port disconnect) can arrive while a
  // connect/reconnect attempt is already in flight. At that moment
  // `session` is still null, so `session?.end()` in the "stop"/
  // onDisconnect handlers below is a no-op — with nothing else, the
  // in-flight attempt would later resolve, assign itself to `session`,
  // and keep running: an orphaned WebSocket and a live gateway session
  // nobody asked for anymore (e.g. Meet navigating away from a call
  // right as a network-blip reconnect happens to be mid-flight). Every
  // place that invalidates "whatever's currently connecting or
  // connected" bumps this; connectWithSession captures it at the start
  // and checks it again after every await, discarding (never assigning
  // to `session`, immediately calling .end()) anything from a
  // superseded generation.
  let generation = 0;

  // The content script's tab can disappear (navigation, tab close, or
  // this very extension being reloaded mid-session) between us deciding
  // to post a message and the call landing, which throws "Attempting to
  // use a disconnected port object". Nothing to do but drop the message.
  function safePost(message) {
    if (disconnected) return;
    try {
      port.postMessage(message);
    } catch {
      disconnected = true;
    }
  }

  // scheduleReconnect backs off exponentially (reconnectDelayMs) and
  // retries connectWithSession, up to RECONNECT_MAX_ATTEMPTS — past
  // that it gives up for good via the same {type:"error"} shape a
  // non-recoverable failure already used before 8.5, which content.js
  // already handles (tears that one participant down; unchanged).
  function scheduleReconnect(err) {
    if (intentionalStop || disconnected) return;
    if (reconnectAttempts >= RECONNECT_MAX_ATTEMPTS) {
      console.error(`MithyaX: giving up after ${reconnectAttempts} reconnect attempts —`, err);
      safePost({ type: "error", message: `gave up reconnecting after ${reconnectAttempts} attempts: ${err}` });
      return;
    }
    reconnectAttempts++;
    const delay = reconnectDelayMs(reconnectAttempts);
    console.warn(`MithyaX: reconnecting in ${delay}ms (attempt ${reconnectAttempts}/${RECONNECT_MAX_ATTEMPTS}) —`, err);
    safePost({ type: "reconnecting", attempt: reconnectAttempts, delayMs: delay });
    reconnectTimer = setTimeout(connectWithSession, delay);
  }

  // connectWithSession is the single entry point for both the original
  // connection and every reconnect attempt after it — resetting `state`
  // fresh each time, since a reconnect always means a brand-new session
  // (see this phase's plan doc: no gateway changes, so there's no
  // accumulated risk history to preserve across the gap) and the badge
  // showing stale numbers from a now-dead session would be misleading.
  async function connectWithSession() {
    reconnectTimer = null;
    state = createDetectorState();
    const myGeneration = generation;

    const config = await getConfig();
    if (myGeneration !== generation) return; // superseded while awaiting storage
    if (!config) {
      // Fresh install, or the user cleared the fields — this isn't a
      // connection failure worth retrying (see scheduleReconnect's own
      // backoff/give-up logic below), it's "setup was never finished."
      // content.js maps this straight to a distinct badge kind rather
      // than the generic "Analysis unavailable" a real connection
      // failure gets (see content.js/ui.js Phase 8.10 changes).
      safePost({ type: "not_configured" });
      return;
    }

    try {
      const newSession = await connectSession(config, {
        onMessage: (msg) => {
          if (myGeneration !== generation) return; // superseded — see `generation`'s doc above
          state = applyMessage(state, msg);
          safePost({ type: "state", state, raw: msg });
        },
        onClose: () => {
          if (myGeneration !== generation) return; // already discarded below; nothing left to do
          session = null;
          if (intentionalStop || disconnected) {
            safePost({ type: "closed" });
            return;
          }
          scheduleReconnect(new Error("session websocket closed unexpectedly"));
        },
        // Diagnostic only — a WebSocket that errors always also closes,
        // so onClose above is the sole reconnect trigger; acting on
        // both would double-schedule a reconnect for one failure.
        onError: (err) => {
          if (myGeneration !== generation) return;
          console.error("MithyaX: session error —", err);
        },
      });

      if (myGeneration !== generation) {
        // A stop/disconnect (or a fresh start superseding this one)
        // happened while connectSession was in flight. This session
        // was never wanted — end it immediately rather than assigning
        // it to `session` and leaving an orphaned WebSocket/gateway
        // session running for a meeting that's already gone.
        newSession.end();
        return;
      }

      session = newSession;
      if (reconnectAttempts > 0) {
        console.warn(`MithyaX: reconnected after ${reconnectAttempts} attempt(s).`);
      }
      reconnectAttempts = 0;
    } catch (err) {
      if (myGeneration !== generation) return; // superseded; don't schedule a reconnect nobody wants
      // Getting here means either the credential exchange itself failed
      // (bad/unset GATEWAY_EXTENSION_TOKEN, gateway unreachable) or
      // POST /api/v1/sessions failed for a reason other than an expired
      // credential (connectSession already retries that case once).
      // Logged here — not just forwarded to content.js via safePost —
      // because this is the service worker's own console, and
      // content.js's tab console is a second, separate place to check.
      // Phase 8.5: this used to give up immediately; now it's treated
      // exactly like an established session dropping, since the same
      // transient causes (gateway mid-restart, brief network loss) can
      // just as easily hit the very first connection attempt.
      console.error("MithyaX: failed to connect —", err);
      session = null;
      scheduleReconnect(err);
    }
  }

  port.onMessage.addListener(async (message) => {
    // Phase 8.9: content.js is the only sender on this port and its
    // isolated-world isolation already keeps the Meet page itself from
    // ever getting a reference to it (see the extension-wide security
    // doc in this file's header) — this isn't a defense against a
    // hostile sender, just against a malformed message (a bug, or a
    // future message type reaching a build that doesn't handle it yet)
    // reaching `message.type` on something that isn't shaped like a
    // message at all and throwing out of this listener.
    if (!message || typeof message.type !== "string") return;

    switch (message.type) {
      case "start":
        if (session || reconnectTimer) return; // already running or a reconnect is already scheduled
        intentionalStop = false;
        reconnectAttempts = 0;
        generation++; // belt-and-suspenders: guarantees a fresh start never inherits a stale in-flight attempt
        await connectWithSession();
        break;

      case "frame":
        if (typeof message.data === "string") session?.sendFrame(message.data);
        break;

      case "audio_chunk":
        if (typeof message.data === "string") session?.sendAudioChunk(message.data);
        break;

      // Phase 8.11: the pilot's one feedback signal — best-effort, not
      // queued or retried. If there's no live session (already stopped)
      // or the gateway isn't reachable right now, the click is simply
      // lost; this is pilot telemetry, not data anything else depends
      // on. Reuses the same getConfig()/getSessionCredential() helpers
      // connectSession already uses, and the same session-credential
      // auth POST /api/v1/sessions/:id/feedback shares with POST
      // /sessions and /sessions/ws (see internal/httpserver/
      // sessionauth.go on the gateway side).
      case "feedback":
        if (session && typeof message.useful === "boolean") {
          try {
            const config = await getConfig();
            if (config) {
              const credential = await getSessionCredential(config);
              await fetch(`${config.gatewayUrl}/api/v1/sessions/${session.sessionId}/feedback`, {
                method: "POST",
                headers: { Authorization: `Bearer ${credential}`, "Content-Type": "application/json" },
                body: JSON.stringify({ useful: message.useful }),
              });
            }
          } catch (err) {
            console.warn("MithyaX: failed to submit feedback —", err);
          }
        }
        break;

      case "stop":
        intentionalStop = true;
        generation++;
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
        session?.end();
        session = null;
        break;
    }
  });

  port.onDisconnect.addListener(() => {
    disconnected = true;
    intentionalStop = true;
    generation++;
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
    session?.end();
    session = null;
  });
});
