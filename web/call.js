const ICE_SERVERS = [{ urls: "stun:stun.l.google.com:19302" }];
const FRAME_SAMPLE_INTERVAL_MS = 1000;
const VERDICT_HISTORY_SIZE = 5;
const FRAME_JPEG_QUALITY = 0.8;

const localVideo = document.getElementById("localVideo");
const videosContainer = document.getElementById("videos");
const statusEl = document.getElementById("status");
const gatewayUrlInput = document.getElementById("gatewayUrl");
const roomInput = document.getElementById("room");
const joinButton = document.getElementById("joinButton");
const hangupButton = document.getElementById("hangupButton");

let ws = null;
let localStream = null;

// One PeerCall per other participant in the room — a mesh call means we
// hold a direct RTCPeerConnection (and video tile) to each of them.
const peers = new Map();
window.peers = peers; // exposed for debugging in the console

const params = new URLSearchParams(window.location.search);
if (params.get("room")) {
  roomInput.value = params.get("room");
}
if (params.get("gateway")) {
  gatewayUrlInput.value = params.get("gateway");
}

function log(message) {
  console.log(message);
  statusEl.textContent += message + "\n";
}

function send(message) {
  ws.send(JSON.stringify(message));
}

function shortId(id) {
  return id ? id.slice(0, 8) : "unknown";
}

function createVideoTile(peerId) {
  const box = document.createElement("div");
  box.className = "video-box";
  box.dataset.peerId = peerId;

  const label = document.createElement("span");
  label.textContent = shortId(peerId);

  const badge = document.createElement("span");
  badge.className = "badge badge-pending";
  badge.textContent = "checking...";

  const video = document.createElement("video");
  video.autoplay = true;
  video.playsInline = true;

  box.appendChild(label);
  box.appendChild(badge);
  box.appendChild(video);
  videosContainer.appendChild(box);

  return { videoEl: video, badgeEl: badge };
}

function removeVideoTile(peerId) {
  const box = videosContainer.querySelector(`[data-peer-id="${peerId}"]`);
  if (box) box.remove();
}

// Grabs the current frame from a <video> as a JPEG blob, for sending to
// the detector. Returns null if the video has no frames yet.
function captureFrame(videoEl) {
  if (!videoEl.videoWidth || !videoEl.videoHeight) {
    return Promise.resolve(null);
  }

  const canvas = document.createElement("canvas");
  canvas.width = videoEl.videoWidth;
  canvas.height = videoEl.videoHeight;
  canvas.getContext("2d").drawImage(videoEl, 0, 0, canvas.width, canvas.height);

  return new Promise((resolve) => {
    canvas.toBlob((blob) => resolve(blob), "image/jpeg", FRAME_JPEG_QUALITY);
  });
}

// Derives the gateway's plain HTTP origin from its WebSocket URL, e.g.
// "ws://localhost:8080/api/v1/ws" -> "http://localhost:8080".
function gatewayHttpOrigin() {
  const wsUrl = new URL(gatewayUrlInput.value);
  const httpProtocol = wsUrl.protocol === "wss:" ? "https:" : "http:";
  return `${httpProtocol}//${wsUrl.host}`;
}

function updateBadge(badgeEl, history) {
  if (history.length === 0) {
    badgeEl.textContent = "checking...";
    badgeEl.className = "badge badge-pending";
    return;
  }

  const fakeVotes = history.filter((verdict) => verdict === "fake").length;
  const isFake = fakeVotes > history.length / 2;

  badgeEl.textContent = isFake ? "🔴 AI" : "🟢 REAL";
  badgeEl.className = "badge " + (isFake ? "badge-fake" : "badge-real");
}

// PeerCall wraps one direct connection to one other participant.
class PeerCall {
  constructor(peerId) {
    this.peerId = peerId;
    this.remoteDescriptionSet = false;
    this.pendingCandidates = [];
    this.verdictHistory = [];
    this.sampleTimer = null;
    this.sampling = false;

    const tile = createVideoTile(peerId);
    this.videoEl = tile.videoEl;
    this.badgeEl = tile.badgeEl;

    this.pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });

    for (const track of localStream.getTracks()) {
      this.pc.addTrack(track, localStream);
    }

    this.pc.ontrack = (event) => {
      this.videoEl.srcObject = event.streams[0];
      this.startSampling();
    };

    this.pc.onicecandidate = (event) => {
      if (event.candidate) {
        send({ type: "ice-candidate", to: peerId, payload: event.candidate });
      }
    };

    this.pc.onconnectionstatechange = () => {
      log(`peer ${shortId(peerId)} connection state: ${this.pc.connectionState}`);
    };
  }

  startSampling() {
    if (this.sampleTimer) {
      return;
    }
    this.sampleTimer = setInterval(() => this.sampleFrame(), FRAME_SAMPLE_INTERVAL_MS);
  }

  stopSampling() {
    clearInterval(this.sampleTimer);
    this.sampleTimer = null;
  }

  // Grabs the current frame from this peer's video, sends it off for a
  // real/fake check, and folds the result into a rolling majority vote —
  // a single noisy frame shouldn't flip the badge on its own.
  async sampleFrame() {
    if (this.sampling) {
      return; // previous request still in flight; skip this tick
    }

    this.sampling = true;
    try {
      const blob = await captureFrame(this.videoEl);
      if (!blob) {
        return;
      }

      const response = await fetch(`${gatewayHttpOrigin()}/api/v1/analyze-frame`, {
        method: "POST",
        headers: { "Content-Type": "image/jpeg" },
        body: blob,
      });
      if (!response.ok) {
        return;
      }

      const result = await response.json();
      if (!result.face_detected) {
        return; // don't let "no face visible" count as evidence either way
      }

      this.verdictHistory.push(result.verdict);
      if (this.verdictHistory.length > VERDICT_HISTORY_SIZE) {
        this.verdictHistory.shift();
      }
      updateBadge(this.badgeEl, this.verdictHistory);
    } catch (err) {
      // Transient network hiccup — just skip this tick.
    } finally {
      this.sampling = false;
    }
  }

  async createOffer() {
    const offer = await this.pc.createOffer();
    await this.pc.setLocalDescription(offer);
    send({ type: "offer", to: this.peerId, payload: offer });
    log(`sent offer to ${shortId(this.peerId)}`);
  }

  async handleOffer(sdp) {
    await this.pc.setRemoteDescription(sdp);
    this.remoteDescriptionSet = true;
    await this.drainPendingCandidates();

    const answer = await this.pc.createAnswer();
    await this.pc.setLocalDescription(answer);
    send({ type: "answer", to: this.peerId, payload: answer });
    log(`sent answer to ${shortId(this.peerId)}`);
  }

  async handleAnswer(sdp) {
    await this.pc.setRemoteDescription(sdp);
    this.remoteDescriptionSet = true;
    await this.drainPendingCandidates();
  }

  async handleCandidate(candidate) {
    if (this.remoteDescriptionSet) {
      await this.pc.addIceCandidate(candidate);
    } else {
      this.pendingCandidates.push(candidate);
    }
  }

  async drainPendingCandidates() {
    while (this.pendingCandidates.length > 0) {
      await this.pc.addIceCandidate(this.pendingCandidates.shift());
    }
  }

  close() {
    this.stopSampling();
    this.pc.close();
    removeVideoTile(this.peerId);
  }
}

function getOrCreatePeer(peerId) {
  let call = peers.get(peerId);
  if (!call) {
    call = new PeerCall(peerId);
    peers.set(peerId, call);
  }
  return call;
}

async function join() {
  const room = roomInput.value.trim();
  if (!room) {
    log("enter a room id first");
    return;
  }

  joinButton.disabled = true;

  localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
  localVideo.srcObject = localStream;

  const url = `${gatewayUrlInput.value}?room=${encodeURIComponent(room)}`;
  ws = new WebSocket(url);

  ws.onopen = () => log(`connected to signaling server, room=${room}`);
  ws.onerror = () => log("signaling connection error");
  ws.onclose = () => log("signaling connection closed");

  ws.onmessage = async (event) => {
    const message = JSON.parse(event.data);

    switch (message.type) {
      case "peers":
        log(`${message.peers ? message.peers.length : 0} participant(s) already here`);
        // We wait passively — each existing peer will send us an offer.
        break;

      case "join":
        log(`peer joined: ${shortId(message.from)}`);
        await getOrCreatePeer(message.from).createOffer();
        break;

      case "offer":
        await getOrCreatePeer(message.from).handleOffer(message.payload);
        break;

      case "answer": {
        const call = peers.get(message.from);
        if (call) {
          await call.handleAnswer(message.payload);
        }
        break;
      }

      case "ice-candidate":
        await getOrCreatePeer(message.from).handleCandidate(message.payload);
        break;

      case "leave": {
        log(`peer left: ${shortId(message.from)}`);
        const call = peers.get(message.from);
        if (call) {
          call.close();
          peers.delete(message.from);
        }
        break;
      }
    }
  };

  hangupButton.disabled = false;
}

function hangup() {
  if (ws) {
    if (ws.readyState === WebSocket.OPEN) {
      send({ type: "leave" });
    }
    ws.close();
    ws = null;
  }

  for (const call of peers.values()) {
    call.close();
  }
  peers.clear();

  if (localStream) {
    localStream.getTracks().forEach((track) => track.stop());
    localStream = null;
  }
  localVideo.srcObject = null;

  joinButton.disabled = false;
  hangupButton.disabled = true;
  log("hung up");
}

joinButton.addEventListener("click", () => {
  join().catch((err) => log("join failed: " + err.message));
});
hangupButton.addEventListener("click", hangup);
