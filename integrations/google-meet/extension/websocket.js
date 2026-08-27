// websocket.js — owns the connection to MithyaX's live-analysis
// session endpoint. Imported by background.js only: the service worker
// holds this connection (not the content script) so it survives Meet's
// SPA navigations and content-script reloads, and so opening a raw
// WebSocket to localhost never has to reckon with meet.google.com's
// page CSP.
//
// Wire format matches web/live-session.js exactly: POST /api/v1/sessions
// to mint a session id, then a WebSocket to
// /api/v1/sessions/ws?session_id=<id> carrying {type:"frame"} and
// {type:"audio_chunk"} out, and video_result/audio_result/
// temporal_result/risk_update back.

// Skip sends while the socket is backed up rather than letting frames
// queue unboundedly if the gateway (or network) falls behind.
const BUFFERED_AMOUNT_LIMIT = 1_000_000;

export class MithyaXSession {
  constructor(gatewayHttpUrl, { onMessage, onClose, onError } = {}) {
    this.gatewayHttpUrl = gatewayHttpUrl.replace(/\/$/, "");
    this.onMessage = onMessage || (() => {});
    this.onClose = onClose || (() => {});
    this.onError = onError || (() => {});
    this.ws = null;
    this.sessionId = null;
  }

  wsUrl(sessionId) {
    const url = new URL(this.gatewayHttpUrl);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.pathname = "/api/v1/sessions/ws";
    url.search = `?session_id=${encodeURIComponent(sessionId)}`;
    return url.toString();
  }

  async connect() {
    const resp = await fetch(`${this.gatewayHttpUrl}/api/v1/sessions`, { method: "POST" });
    if (!resp.ok) {
      throw new Error(`failed to create session: HTTP ${resp.status}`);
    }
    const { id } = await resp.json();
    this.sessionId = id;

    await new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.wsUrl(id));
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
