// Phase 4 reference client for MithyaX's live analysis session: create
// a session, stream this browser's own camera/mic to it over
// /api/v1/sessions/ws, and render the video/audio/temporal/risk
// updates as they arrive.
//
// The frame-capture step (captureFrame, below) is the same
// canvas.toBlob("image/jpeg") technique call.js already uses to sample
// a peer's remote video for /api/v1/analyze-frame — reused here
// verbatim, just fed into a persistent session instead of a one-shot
// POST per frame.

const FRAME_SAMPLE_INTERVAL_MS = 1000;
const FRAME_JPEG_QUALITY = 0.8;
// The audio-detector currently only decodes WAV (see
// services/audio-detector/app/audio/decoder.py) — MediaRecorder's
// webm/opus output wouldn't work, so this captures raw PCM via the Web
// Audio API and encodes WAV chunks by hand instead.
const AUDIO_CHUNK_SECONDS = 3;

const localVideo = document.getElementById("localVideo");
const statusEl = document.getElementById("status");
const gatewayUrlInput = document.getElementById("gatewayUrl");
const startButton = document.getElementById("startButton");
const stopButton = document.getElementById("stopButton");
const verdictBadge = document.getElementById("verdictBadge");
const videoScoreEl = document.getElementById("videoScore");
const audioScoreEl = document.getElementById("audioScore");
const temporalScoreEl = document.getElementById("temporalScore");
const riskScoreEl = document.getElementById("riskScore");
const reasonsEl = document.getElementById("reasons");

let ws = null;
let localStream = null;
let frameTimer = null;
let sampling = false;

let audioContext = null;
let audioSourceNode = null;
let audioProcessorNode = null;
let audioSilentGain = null;
let pendingAudioChunks = [];

function log(message) {
  console.log(message);
  statusEl.textContent += message + "\n";
  statusEl.scrollTop = statusEl.scrollHeight;
}

function send(message) {
  ws.send(JSON.stringify(message));
}

function gatewayWsOrigin() {
  const url = new URL(gatewayUrlInput.value);
  const wsProtocol = url.protocol === "https:" ? "wss:" : "ws:";
  return `${wsProtocol}//${url.host}`;
}

// --- reused from call.js: grab the current frame from a <video> as a JPEG blob ---
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

async function sampleFrame() {
  if (sampling) {
    return; // previous frame still uploading; skip this tick
  }
  sampling = true;
  try {
    const blob = await captureFrame(localVideo);
    if (!blob) {
      return;
    }
    const data = await blobToBase64(blob);
    send({ type: "frame", data });
  } catch (err) {
    log(`frame capture failed: ${err.message}`);
  } finally {
    sampling = false;
  }
}

function startFrameSampling() {
  stopFrameSampling();
  frameTimer = setInterval(sampleFrame, FRAME_SAMPLE_INTERVAL_MS);
}

function stopFrameSampling() {
  clearInterval(frameTimer);
  frameTimer = null;
}

// --- WAV encoding: audio-detector decodes WAV only, see decoder.py ---
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

async function flushAudioChunk() {
  if (pendingAudioChunks.length === 0) {
    return;
  }
  const samples = mergeFloat32(pendingAudioChunks);
  pendingAudioChunks = [];

  const wavBlob = encodeWav(samples, audioContext.sampleRate);
  const data = await blobToBase64(wavBlob);
  send({ type: "audio_chunk", data, filename: "chunk.wav" });
}

function startAudioCapture(stream) {
  audioContext = new (window.AudioContext || window.webkitAudioContext)();
  audioSourceNode = audioContext.createMediaStreamSource(stream);

  // ScriptProcessorNode is deprecated in favor of AudioWorklet, but
  // needs no separate module file to load — simplest thing that works
  // reliably across browsers for this reference client.
  audioProcessorNode = audioContext.createScriptProcessor(4096, 1, 1);
  pendingAudioChunks = [];

  audioProcessorNode.onaudioprocess = (event) => {
    pendingAudioChunks.push(new Float32Array(event.inputBuffer.getChannelData(0)));

    const totalSamples = pendingAudioChunks.reduce((sum, chunk) => sum + chunk.length, 0);
    if (totalSamples >= audioContext.sampleRate * AUDIO_CHUNK_SECONDS) {
      flushAudioChunk();
    }
  };

  // Chrome only fires onaudioprocess while the node is connected all
  // the way to a destination. Routing through a zero-gain node keeps
  // the mic audio from being played back to the user.
  audioSilentGain = audioContext.createGain();
  audioSilentGain.gain.value = 0;

  audioSourceNode.connect(audioProcessorNode);
  audioProcessorNode.connect(audioSilentGain);
  audioSilentGain.connect(audioContext.destination);
}

function stopAudioCapture() {
  if (audioProcessorNode) {
    audioProcessorNode.disconnect();
    audioProcessorNode.onaudioprocess = null;
    audioProcessorNode = null;
  }
  if (audioSourceNode) {
    audioSourceNode.disconnect();
    audioSourceNode = null;
  }
  if (audioSilentGain) {
    audioSilentGain.disconnect();
    audioSilentGain = null;
  }
  if (audioContext) {
    audioContext.close();
    audioContext = null;
  }
  pendingAudioChunks = [];
}

function setVerdict(verdict) {
  const classes = {
    LIKELY_AUTHENTIC: ["verdict-authentic", "REAL"],
    SUSPICIOUS: ["verdict-suspicious", "SUSPICIOUS"],
    LIKELY_FAKE: ["verdict-fake", "AI"],
    UNKNOWN: ["verdict-unknown", "UNKNOWN"],
  };
  const [className, label] = classes[verdict] || classes.UNKNOWN;
  verdictBadge.className = "verdict " + className;
  verdictBadge.textContent = label;
}

function renderReasons(reasons) {
  reasonsEl.innerHTML = "";
  for (const reason of reasons || []) {
    const li = document.createElement("li");
    li.textContent = "• " + reason;
    reasonsEl.appendChild(li);
  }
}

function handleMessage(message) {
  switch (message.type) {
    case "session_started":
      log(`session started: ${message.id}`);
      break;

    case "video_result":
      videoScoreEl.textContent = message.face_detected
        ? message.fake_score.toFixed(3) + ` (${message.verdict})`
        : "no face detected";
      break;

    case "audio_result":
      audioScoreEl.textContent = `${message.fake_score.toFixed(3)} (${message.verdict})`;
      break;

    case "temporal_result":
      temporalScoreEl.textContent = `${message.score.toFixed(3)} (${message.frames_analyzed} frames)`;
      break;

    case "risk_update":
      riskScoreEl.textContent = message.risk_score.toFixed(3);
      setVerdict(message.verdict);
      renderReasons(message.reasons);
      break;

    case "session_ended":
      log("session ended");
      teardown();
      break;

    case "error":
      log(`error: ${message.message}`);
      break;

    default:
      log(`unhandled message type: ${message.type}`);
  }
}

async function start() {
  startButton.disabled = true;

  try {
    localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
    localVideo.srcObject = localStream;

    const createResp = await fetch(`${gatewayUrlInput.value}/api/v1/sessions`, { method: "POST" });
    if (!createResp.ok) {
      throw new Error(`failed to create session: HTTP ${createResp.status}`);
    }
    const { id: sessionId } = await createResp.json();
    log(`created session ${sessionId}`);

    const wsUrl = `${gatewayWsOrigin()}/api/v1/sessions/ws?session_id=${encodeURIComponent(sessionId)}`;
    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
      log("connected to session websocket");
      startFrameSampling();
      startAudioCapture(localStream);
    };
    ws.onerror = () => log("session websocket error");
    ws.onclose = () => {
      log("session websocket closed");
      teardown();
    };
    ws.onmessage = (event) => handleMessage(JSON.parse(event.data));

    stopButton.disabled = false;
  } catch (err) {
    log(`start failed: ${err.message}`);
    teardown();
    startButton.disabled = false;
  }
}

function stop() {
  if (ws && ws.readyState === WebSocket.OPEN) {
    send({ type: "end_session" });
  } else {
    teardown();
  }
}

function teardown() {
  stopFrameSampling();
  stopAudioCapture();

  if (ws) {
    ws.close();
    ws = null;
  }
  if (localStream) {
    localStream.getTracks().forEach((track) => track.stop());
    localStream = null;
  }
  localVideo.srcObject = null;

  startButton.disabled = false;
  stopButton.disabled = true;
}

startButton.addEventListener("click", () => {
  start().catch((err) => log("start failed: " + err.message));
});
stopButton.addEventListener("click", stop);
