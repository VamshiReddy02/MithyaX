const ICE_SERVERS = [{ urls: "stun:stun.l.google.com:19302" }];
const FRAME_SAMPLE_INTERVAL_MS = 1000;
const FRAME_JPEG_QUALITY = 0.8;
// The audio-detector currently only decodes WAV (see
// services/audio-detector/app/audio/decoder.py) — raw PCM is captured
// via the Web Audio API and encoded to WAV by hand rather than using
// MediaRecorder's webm/opus output.
const AUDIO_CHUNK_SECONDS = 3;

const localVideo = document.getElementById("localVideo");
const videosContainer = document.getElementById("videos");
const statusEl = document.getElementById("status");
const gatewayUrlInput = document.getElementById("gatewayUrl");
const roomInput = document.getElementById("room");
const cameraSelect = document.getElementById("cameraSelect");
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

// Device labels are only populated once permission has been granted at
// least once on this origin — before that they show up blank. We call
// this on load (best effort) and again right after the first
// getUserMedia() call, plus on devicechange so a camera that starts up
// later (e.g. a virtual camera app launched after the page loaded) shows
// up without a reload.
async function refreshCameraList() {
  const devices = await navigator.mediaDevices.enumerateDevices();
  const cameras = devices.filter((d) => d.kind === "videoinput");

  const previouslySelected = cameraSelect.value;
  cameraSelect.innerHTML = "";

  const defaultOption = document.createElement("option");
  defaultOption.value = "";
  defaultOption.textContent = "Default camera";
  cameraSelect.appendChild(defaultOption);

  cameras.forEach((device, index) => {
    const option = document.createElement("option");
    option.value = device.deviceId;
    option.textContent = device.label || `Camera ${index + 1}`;
    cameraSelect.appendChild(option);
  });

  if (cameras.some((d) => d.deviceId === previouslySelected)) {
    cameraSelect.value = previouslySelected;
  }
}

navigator.mediaDevices.addEventListener("devicechange", refreshCameraList);
refreshCameraList().catch(() => {});

function createVideoTile(peerId) {
  const box = document.createElement("div");
  box.className = "video-box";
  box.dataset.peerId = peerId;

  const label = document.createElement("span");
  label.textContent = shortId(peerId);

  const badge = document.createElement("span");
  badge.className = "badge badge-pending";
  badge.textContent = "checking...";

  const scoreLine = document.createElement("span");
  scoreLine.className = "score-line";
  scoreLine.textContent = "";

  const video = document.createElement("video");
  video.autoplay = true;
  video.playsInline = true;

  box.appendChild(label);
  box.appendChild(badge);
  box.appendChild(scoreLine);
  box.appendChild(video);
  videosContainer.appendChild(box);

  return { videoEl: video, badgeEl: badge, scoreEl: scoreLine };
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

function blobToBase64(blob) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onloadend = () => resolve(reader.result.split(",")[1]);
    reader.onerror = reject;
    reader.readAsDataURL(blob);
  });
}

// --- WAV encoding: the audio-detector decodes WAV only (see decoder.py) ---
function writeAsciiString(view, offset, str) {
  for (let i = 0; i < str.length; i++) {
    view.setUint8(offset + i, str.charCodeAt(i));
  }
}

function encodeWav(samples, sampleRate) {
  const buffer = new ArrayBuffer(44 + samples.length * 2);
  const view = new DataView(buffer);

  writeAsciiString(view, 0, "RIFF");
  view.setUint32(4, 36 + samples.length * 2, true);
  writeAsciiString(view, 8, "WAVE");
  writeAsciiString(view, 12, "fmt ");
  view.setUint32(16, 16, true); // fmt chunk size
  view.setUint16(20, 1, true); // PCM
  view.setUint16(22, 1, true); // mono
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * 2, true); // byte rate (16-bit mono)
  view.setUint16(32, 2, true); // block align
  view.setUint16(34, 16, true); // bits per sample
  writeAsciiString(view, 36, "data");
  view.setUint32(40, samples.length * 2, true);

  let offset = 44;
  for (let i = 0; i < samples.length; i++, offset += 2) {
    const clamped = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(offset, clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff, true);
  }

  return new Blob([view], { type: "audio/wav" });
}

function mergeFloat32(chunks) {
  const total = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const merged = new Float32Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.length;
  }
  return merged;
}

// Derives the gateway's plain HTTP/WS origins from the signaling URL
// entered in the UI, e.g. "ws://localhost:8080/api/v1/ws" ->
// "http://localhost:8080" / "ws://localhost:8080".
function gatewayHttpOrigin() {
  const wsUrl = new URL(gatewayUrlInput.value);
  const httpProtocol = wsUrl.protocol === "wss:" ? "https:" : "http:";
  return `${httpProtocol}//${wsUrl.host}`;
}

function gatewayWsOrigin() {
  const wsUrl = new URL(gatewayUrlInput.value);
  return `${wsUrl.protocol}//${wsUrl.host}`;
}

const VERDICT_BADGES = {
  LIKELY_AUTHENTIC: ["badge-real", "🟢 REAL"],
  SUSPICIOUS: ["badge-suspicious", "🟡 SUSPICIOUS"],
  LIKELY_FAKE: ["badge-fake", "🔴 AI"],
  UNKNOWN: ["badge-pending", "checking..."],
};

function renderRiskUpdate(badgeEl, scoreEl, message) {
  const [className, label] = VERDICT_BADGES[message.verdict] || VERDICT_BADGES.UNKNOWN;
  badgeEl.className = "badge " + className;
  badgeEl.textContent = label;
  scoreEl.textContent = `risk ${message.risk_score.toFixed(2)}`;
  scoreEl.title = (message.reasons || []).join("\n");
}

// PeerCall wraps one direct connection to one other participant. Once
// their remote stream arrives, it opens a MithyaX live analysis session
// (POST /api/v1/sessions + WebSocket /api/v1/sessions/ws) and streams
// that peer's own remote video frames and audio into it — this is the
// "remote video/audio → MithyaX" path from the real-time architecture,
// as opposed to web/live-session.js's demo of analyzing your own local
// camera.
class PeerCall {
  constructor(peerId) {
    this.peerId = peerId;
    this.remoteDescriptionSet = false;
    this.pendingCandidates = [];

    this.analysisStarted = false;
    this.sessionWs = null;
    this.frameTimer = null;
    this.sampling = false;

    this.audioContext = null;
    this.audioSourceNode = null;
    this.audioProcessorNode = null;
    this.audioSilentGain = null;
    this.pendingAudioChunks = [];

    const tile = createVideoTile(peerId);
    this.videoEl = tile.videoEl;
    this.badgeEl = tile.badgeEl;
    this.scoreEl = tile.scoreEl;

    this.pc = new RTCPeerConnection({ iceServers: ICE_SERVERS });

    for (const track of localStream.getTracks()) {
      this.pc.addTrack(track, localStream);
    }

    this.pc.ontrack = (event) => {
      this.videoEl.srcObject = event.streams[0];
      // ontrack fires once per track (video and audio separately, even
      // though both belong to the same remote stream) — guard so the
      // session is only created and its capture pipelines only started
      // once per peer, not once per track.
      if (!this.analysisStarted) {
        this.analysisStarted = true;
        this.startAnalysisSession(event.streams[0]);
      }
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

  // Creates a MithyaX live session for this peer's remote stream and
  // starts streaming frames/audio into it once the WebSocket is open.
  async startAnalysisSession(remoteStream) {
    try {
      const response = await fetch(`${gatewayHttpOrigin()}/api/v1/sessions`, { method: "POST" });
      if (!response.ok) {
        throw new Error(`failed to create session: HTTP ${response.status}`);
      }
      const { id: sessionId } = await response.json();
      log(`analysis session ${shortId(sessionId)} created for peer ${shortId(this.peerId)}`);

      const wsUrl = `${gatewayWsOrigin()}/api/v1/sessions/ws?session_id=${encodeURIComponent(sessionId)}`;
      this.sessionWs = new WebSocket(wsUrl);

      this.sessionWs.onopen = () => {
        this.startFrameSampling();
        if (remoteStream.getAudioTracks().length > 0) {
          this.startAudioCapture(remoteStream);
        }
      };
      this.sessionWs.onmessage = (event) => this.handleSessionMessage(JSON.parse(event.data));
      this.sessionWs.onerror = () => log(`analysis session error for peer ${shortId(this.peerId)}`);
      this.sessionWs.onclose = () => log(`analysis session closed for peer ${shortId(this.peerId)}`);
    } catch (err) {
      log(`failed to start analysis session for ${shortId(this.peerId)}: ${err.message}`);
    }
  }

  sendSessionMessage(message) {
    if (this.sessionWs && this.sessionWs.readyState === WebSocket.OPEN) {
      this.sessionWs.send(JSON.stringify(message));
    }
  }

  handleSessionMessage(message) {
    switch (message.type) {
      case "risk_update":
        renderRiskUpdate(this.badgeEl, this.scoreEl, message);
        break;
      case "error":
        log(`analysis error for peer ${shortId(this.peerId)}: ${message.message}`);
        break;
      // video_result / audio_result / temporal_result / session_started /
      // session_ended carry no UI of their own here — risk_update, sent
      // after each of them, is what drives the badge.
    }
  }

  startFrameSampling() {
    if (this.frameTimer) {
      return;
    }
    this.frameTimer = setInterval(() => this.sampleFrame(), FRAME_SAMPLE_INTERVAL_MS);
  }

  stopFrameSampling() {
    clearInterval(this.frameTimer);
    this.frameTimer = null;
  }

  // Grabs the current frame from this peer's video and streams it into
  // the analysis session.
  async sampleFrame() {
    if (this.sampling) {
      return; // previous frame still uploading; skip this tick
    }
    this.sampling = true;
    try {
      const blob = await captureFrame(this.videoEl);
      if (!blob) {
        return;
      }
      const data = await blobToBase64(blob);
      this.sendSessionMessage({ type: "frame", data });
    } catch (err) {
      // Transient hiccup — just skip this tick.
    } finally {
      this.sampling = false;
    }
  }

  startAudioCapture(stream) {
    this.audioContext = new (window.AudioContext || window.webkitAudioContext)();
    this.audioSourceNode = this.audioContext.createMediaStreamSource(stream);

    // ScriptProcessorNode is deprecated in favor of AudioWorklet, but
    // needs no separate module file to load — simplest thing that works
    // reliably across browsers for this reference client.
    this.audioProcessorNode = this.audioContext.createScriptProcessor(4096, 1, 1);
    this.pendingAudioChunks = [];

    this.audioProcessorNode.onaudioprocess = (event) => {
      this.pendingAudioChunks.push(new Float32Array(event.inputBuffer.getChannelData(0)));

      const totalSamples = this.pendingAudioChunks.reduce((sum, chunk) => sum + chunk.length, 0);
      if (totalSamples >= this.audioContext.sampleRate * AUDIO_CHUNK_SECONDS) {
        this.flushAudioChunk();
      }
    };

    // Some browsers only fire onaudioprocess while the node is connected
    // all the way to a destination. Routing through a zero-gain node
    // avoids playing the peer's own audio back out a second time (their
    // <video> element already plays it).
    this.audioSilentGain = this.audioContext.createGain();
    this.audioSilentGain.gain.value = 0;

    this.audioSourceNode.connect(this.audioProcessorNode);
    this.audioProcessorNode.connect(this.audioSilentGain);
    this.audioSilentGain.connect(this.audioContext.destination);
  }

  async flushAudioChunk() {
    if (this.pendingAudioChunks.length === 0) {
      return;
    }
    const samples = mergeFloat32(this.pendingAudioChunks);
    this.pendingAudioChunks = [];

    const wavBlob = encodeWav(samples, this.audioContext.sampleRate);
    const data = await blobToBase64(wavBlob);
    this.sendSessionMessage({ type: "audio_chunk", data, filename: "chunk.wav" });
  }

  stopAudioCapture() {
    if (this.audioProcessorNode) {
      this.audioProcessorNode.disconnect();
      this.audioProcessorNode.onaudioprocess = null;
      this.audioProcessorNode = null;
    }
    if (this.audioSourceNode) {
      this.audioSourceNode.disconnect();
      this.audioSourceNode = null;
    }
    if (this.audioSilentGain) {
      this.audioSilentGain.disconnect();
      this.audioSilentGain = null;
    }
    if (this.audioContext) {
      this.audioContext.close();
      this.audioContext = null;
    }
    this.pendingAudioChunks = [];
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
    this.stopFrameSampling();
    this.stopAudioCapture();

    if (this.sessionWs) {
      this.sendSessionMessage({ type: "end_session" });
      this.sessionWs.close();
      this.sessionWs = null;
    }

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

  const selectedCamera = cameraSelect.value;
  const videoConstraints = selectedCamera ? { deviceId: { exact: selectedCamera } } : true;
  localStream = await navigator.mediaDevices.getUserMedia({ video: videoConstraints, audio: true });
  localVideo.srcObject = localStream;

  // Now that permission is granted, device labels are populated — refresh
  // so the dropdown shows real names instead of "Camera 1", "Camera 2".
  refreshCameraList().catch(() => {});

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
