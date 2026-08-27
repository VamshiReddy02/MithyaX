// content.js — orchestrator. Runs inside the Meet tab, polls the DOM for
// the remote participant's media elements, and drives capture.js (frame
// + audio capture) and ui.js (badge) through a long-lived port to
// background.js, which owns the actual MithyaX session.
(function () {
  const FRAME_SAMPLE_INTERVAL_MS = 1000;
  const AUDIO_CHUNK_SECONDS = 3;
  const DETECT_INTERVAL_MS = 2000;
  const POSITION_INTERVAL_MS = 500;

  const { captureFrameBase64, RemoteAudioCapture } = window.__mithyax.capture;
  const { updateBadge, removeBadge, positionBadge } = window.__mithyax.ui;

  let port = null;
  let frameTimer = null;
  let positionTimer = null;
  let sampling = false;
  let audioCapture = null;
  let currentVideoEl = null;
  let started = false;
  let killed = false;

  // Reloading the extension while this tab is already connected
  // invalidates its runtime — any chrome.runtime.* call after that
  // throws "Extension context invalidated". There's no recovering
  // short of the user reloading the tab, so stop polling instead of
  // throwing on every tick.
  function handleFatal(err) {
    if (killed) return;
    killed = true;
    console.warn("MithyaX: extension context lost — reload this tab to reconnect.", err);
    stopCapture();
  }

  // Meet doesn't hand pages a raw WebRTC track for remote participants —
  // it builds its own MediaStreamTrack (via WebCodecs/insertable
  // streams) and labels it "remote video"/"remote audio". The local
  // camera/mic preview keeps its real device name (e.g. "MacBook Pro
  // Camera (0000:0001)"). Matching on "remote" is how real-world Meet
  // extensions tell the two apart; confirmed against a live call via
  // devtools rather than assumed.
  function isRemoteTrack(track) {
    return !!track && /remote/i.test(track.label);
  }

  function findRemoteVideoElement() {
    const candidates = Array.from(document.querySelectorAll("video")).filter((el) => {
      const stream = el.srcObject;
      if (!(stream instanceof MediaStream)) return false;
      const [track] = stream.getVideoTracks();
      return isRemoteTrack(track) && el.videoWidth > 0;
    });
    if (candidates.length === 0) return null;

    // Group calls can have several remote tiles; picking the largest
    // on-screen one is a stand-in for "the active speaker" — good
    // enough for proving the single-participant pipeline, not real
    // multi-party handling.
    return candidates.sort((a, b) => b.clientWidth * b.clientHeight - a.clientWidth * a.clientHeight)[0];
  }

  function findRemoteAudioStream() {
    const elements = [...document.querySelectorAll("audio"), ...document.querySelectorAll("video")];
    for (const el of elements) {
      const stream = el.srcObject;
      if (!(stream instanceof MediaStream)) continue;
      const [track] = stream.getAudioTracks();
      if (isRemoteTrack(track)) return stream;
    }
    return null;
  }

  function connectPort() {
    if (port) return port;
    try {
      port = chrome.runtime.connect({ name: "mithyax" });
    } catch (err) {
      handleFatal(err);
      return null;
    }
    port.onMessage.addListener((message) => {
      if (message.type === "state") updateBadge(message.state);
    });
    port.onDisconnect.addListener(() => {
      port = null;
      started = false;
    });
    return port;
  }

  async function sampleFrame() {
    if (sampling || !currentVideoEl || !port) return;
    sampling = true;
    try {
      const data = await captureFrameBase64(currentVideoEl);
      if (data) port.postMessage({ type: "frame", data });
    } catch (err) {
      handleFatal(err);
    } finally {
      sampling = false;
    }
  }

  function startCapture() {
    if (started || killed) return;
    const p = connectPort();
    if (!p) return;
    started = true;
    try {
      p.postMessage({ type: "start" });
    } catch (err) {
      handleFatal(err);
      return;
    }
    frameTimer = setInterval(sampleFrame, FRAME_SAMPLE_INTERVAL_MS);
    positionTimer = setInterval(() => positionBadge(currentVideoEl), POSITION_INTERVAL_MS);
    updateBadge({ tier: "unknown", label: "Analyzing" });
    positionBadge(currentVideoEl);
  }

  function stopCapture() {
    started = false;
    clearInterval(frameTimer);
    frameTimer = null;
    clearInterval(positionTimer);
    positionTimer = null;
    if (audioCapture) {
      audioCapture.stop();
      audioCapture = null;
    }
    currentVideoEl = null;
    if (port) {
      const p = port;
      port = null;
      try {
        p.postMessage({ type: "stop" });
        p.disconnect();
      } catch {
        // already gone — nothing to clean up
      }
    }
    removeBadge();
  }

  function tick() {
    if (killed) return;
    const videoEl = findRemoteVideoElement();
    currentVideoEl = videoEl;

    if (videoEl && !started) {
      startCapture();
    } else if (!videoEl && started) {
      stopCapture();
      return;
    }

    if (started && !audioCapture) {
      const stream = findRemoteAudioStream();
      if (stream) {
        audioCapture = new RemoteAudioCapture(stream, {
          chunkSeconds: AUDIO_CHUNK_SECONDS,
          onChunk: (data) => {
            if (!port) return;
            try {
              port.postMessage({ type: "audio_chunk", data });
            } catch (err) {
              handleFatal(err);
            }
          },
        });
      }
    }
  }

  setInterval(tick, DETECT_INTERVAL_MS);
  tick();
})();
