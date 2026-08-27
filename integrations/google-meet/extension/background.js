// background.js — the extension's service worker. Owns one MithyaXSession
// per Meet tab (keyed by the runtime.Port that tab's content.js opens)
// and the running detector state for it, and relays between the two:
// capture data in from content.js, labeled risk state back out to it.
//
// Edit GATEWAY_HTTP_URL if the gateway isn't on localhost:8080 — there's
// no settings UI yet (see README in this directory).
import { MithyaXSession } from "./websocket.js";
import { createDetectorState, applyMessage } from "./detector.js";

const GATEWAY_HTTP_URL = "http://localhost:8080";

chrome.runtime.onConnect.addListener((port) => {
  if (port.name !== "mithyax") return;

  let state = createDetectorState();
  let session = null;
  let disconnected = false;

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

  port.onMessage.addListener(async (message) => {
    switch (message.type) {
      case "start": {
        if (session) return;
        session = new MithyaXSession(GATEWAY_HTTP_URL, {
          onMessage: (msg) => {
            state = applyMessage(state, msg);
            safePost({ type: "state", state, raw: msg });
          },
          onClose: () => safePost({ type: "closed" }),
          onError: (err) => safePost({ type: "error", message: String(err) }),
        });
        try {
          await session.connect();
        } catch (err) {
          safePost({ type: "error", message: String(err) });
          session = null;
        }
        break;
      }

      case "frame":
        session?.sendFrame(message.data);
        break;

      case "audio_chunk":
        session?.sendAudioChunk(message.data);
        break;

      case "stop":
        session?.end();
        session = null;
        break;
    }
  });

  port.onDisconnect.addListener(() => {
    disconnected = true;
    session?.end();
    session = null;
  });
});
