// websocket.js — owns the connection to MithyaX's live-analysis
// session endpoint. Imported by background.js only: the service worker
// holds this connection (not the content script) so it survives Meet's
// SPA navigations and content-script reloads, and so opening a raw
// WebSocket to localhost never has to reckon with meet.google.com's
// page CSP.
//
// Wire format matches web/live-session.js, plus the short-lived session
// credential Phase 8.1 added on the gateway side (see background.js,
// which is the only place that ever holds the extension's long-lived
// token and exchanges it for one of these): POST /api/v1/sessions
// carrying "Authorization: Bearer <credential>" to mint a session id,
// then a WebSocket to
// /api/v1/sessions/ws?session_id=<id>&credential=<credential> — a
// query parameter there rather than a header, since a browser
// WebSocket can't attach one to its handshake — carrying
// {type:"frame"} and {type:"audio_chunk"} out, and video_result/
// audio_result/temporal_result/risk_update back. This class never
// fetches or caches the credential itself — connect() takes one
// already in hand, so credential lifetime/refresh stays entirely
// background.js's concern.

// Skip sends while the socket is backed up rather than letting frames
// queue unboundedly if the gateway (or network) falls behind.
const BUFFERED_AMOUNT_LIMIT = 1_000_000;

// SessionCreateError carries the HTTP status POST /api/v1/sessions
// failed with, so background.js can tell "the credential was rejected
// (401) — worth minting a fresh one and retrying once" apart from any
// other failure (network down, gateway 5xx, ...), which isn't.
export class SessionCreateError extends Error {
  constructor(status, message) {
    super(message);
    this.name = "SessionCreateError";
    this.status = status;
  }
}

export class MithyaXSession {
  constructor(gatewayHttpUrl, { onMessage, onClose, onError } = {}) {
    this.gatewayHttpUrl = gatewayHttpUrl.replace(/\/$/, "");
    this.onMessage = onMessage || (() => {});
    this.onClose = onClose || (() => {});
    this.onError = onError || (() => {});
    this.ws = null;
    this.sessionId = null;
  }

  wsUrl(sessionId, credential) {
    const url = new URL(this.gatewayHttpUrl);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.pathname = "/api/v1/sessions/ws";
    url.search = `?${new URLSearchParams({ session_id: sessionId, credential })}`;
    return url.toString();
  }

  // connect authenticates with credential — a short-lived session
  // credential from POST /api/v1/auth/session, never the extension's
  // own long-lived token, which this class never sees.
  async connect(credential) {
    const resp = await fetch(`${this.gatewayHttpUrl}/api/v1/sessions`, {
      method: "POST",
      headers: { Authorization: `Bearer ${credential}` },
    });
    if (!resp.ok) {
      throw new SessionCreateError(resp.status, `failed to create session: HTTP ${resp.status}`);
    }
    const { id } = await resp.json();
    this.sessionId = id;

    await new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.wsUrl(id, credential));
      this.ws.onopen = () => resolve();
      this.ws.onerror = (event) => {
        this.onError(event);
        reject(new Error("session websocket error"));
      };
      this.ws.onclose = () => this.onClose();
      this.ws.onmessage = (event) => {
        try {
          this.onMessage(JSON.parse(event.data));
        } catch (err) {
          this.onError(err);
        }
      };
    });
  }

  send(message) {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return false;
    if (this.ws.bufferedAmount > BUFFERED_AMOUNT_LIMIT) return false;
    this.ws.send(JSON.stringify(message));
    return true;
  }

  sendFrame(data) {
    return this.send({ type: "frame", data });
  }

  sendAudioChunk(data) {
    return this.send({ type: "audio_chunk", data, filename: "chunk.wav" });
  }

  end() {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.send({ type: "end_session" });
      this.ws.close();
    }
    this.ws = null;
  }
}
