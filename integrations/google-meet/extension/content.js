// content.js — orchestrator. Runs inside the Meet tab, polls the DOM for
// the remote participant's media elements, and drives capture.js (frame
// + audio capture) and ui.js (badge) through a long-lived port to
// background.js, which owns the actual MithyaX session.
//
// Phase 8.3 hardening: tick() used to treat a single missed detection —
// findRemoteVideoElement() returning null on one 2s poll — identically
// to a genuine "participant left," tearing the whole session down
// (closing the WebSocket, discarding the risk state, restarting from
// "Connecting…"). That's indistinguishable, from one poll's point of
// view, from the DOM transiently not matching for a tick: the tile's
// <video> element being replaced, a camera blip, or Meet's SPA
// navigation reshuffling the call view. PARTICIPANT_LEFT_GRACE_TICKS
// below turns that into "N consecutive misses," so a session survives a
// few seconds of "can't currently find it" and only actually ends once
// the remote participant is truly gone. Audio had the opposite gap —
// findRemoteAudioStream() was only ever consulted once (`!audioCapture`
// guarded it forever after), so a replaced <audio> element's new stream
// was never picked up; refreshAudioCapture() now re-checks every tick
// and swaps in a fresh RemoteAudioCapture whenever the stream identity
// actually changes.
(function () {
  const FRAME_SAMPLE_INTERVAL_MS = 1000;
  const AUDIO_CHUNK_SECONDS = 3;
  const DETECT_INTERVAL_MS = 2000;
  const POSITION_INTERVAL_MS = 500;
  // ~6s at DETECT_INTERVAL_MS=2000ms: long enough to ride out a DOM
  // element swap or a several-second camera-off blip without losing the
  // session's continuity (a fresh session means a fresh WebSocket and a
  // reset temporal/risk state), short enough that an actual "left" is
  // still noticed within a few seconds.
  const PARTICIPANT_LEFT_GRACE_TICKS = 3;

  const { captureFrameBase64, RemoteAudioCapture } = window.__mithyax.capture;
  const { updateBadge, removeBadge, positionBadge } = window.__mithyax.ui;

  let port = null;
  let frameTimer = null;
  let positionTimer = null;
  let sampling = false;
  let audioCapture = null;
  let audioStreamRef = null; // the MediaStream audioCapture is currently bound to
  let currentVideoEl = null;
  let missedVideoTicks = 0;
  let started = false;
  let killed = false;

  // Stops everything permanently for the rest of this tab's lifetime —
  // for anything with no recovery short of the user reloading the tab.
  // Originally just extension-context invalidation (reloading the
  // extension while this tab is already connected invalidates its
  // runtime; any chrome.runtime.* call after that throws "Extension
  // context invalidated") but also used as the fail-safe stop for
  // sampleFrame's remote-track re-verification — deliberately the same
  // "stop and require a reload" response, not a silent recovery
  // attempt, given what that check guards against.
  function handleFatal(err) {
    if (killed) return;
    killed = true;
    console.warn("MithyaX: stopping — reload this tab to reconnect.", err);
    stopCapture();
  }

  // Meet doesn't hand pages a raw WebRTC track for remote participants —
  // it builds its own MediaStreamTrack (via WebCodecs/insertable
  // streams) and labels it "remote video"/"remote audio". The local
  // camera/mic preview keeps its real device name (e.g. "MacBook Pro
  // Camera (0000:0001)"). Matching on "remote" is how real-world Meet
  // extensions tell the two apart; confirmed against a live call via
  // devtools rather than assumed.
  //
  // This is the one check standing between "analyze the remote
  // participant" and "analyze the user's own camera/mic" — an allow-list
  // (a track must positively match to be treated as remote, rather than
  // merely failing to look local) is the safer shape for that, but a
  // label match is still a heuristic pointed at Meet's internals, not a
  // documented API. Anchored to the start of the label (Meet's own
  // strings are exactly "remote video"/"remote audio", not "remote"
  // appearing elsewhere in a longer string) specifically to shrink the
  // one real residual risk: a real local device product name that
  // happens to *contain* "remote" (e.g. a "Remote Desktop Virtual
  // Camera" driver) would no longer false-positive here purely by
  // substring match. This still hasn't been re-verified against a live
  // two-party call since that tightening — do that before relying on it
  // for anything beyond this proof-of-concept.
  function isRemoteTrack(track) {
    return !!track && /^remote/i.test(track.label);
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
    // multi-party handling (8.3 explicitly defers building independent
    // sessions per participant). Logged, not silently dropped, so
    // multi-participant calls are at least visibly detected during
    // development even though only one is ever analyzed.
    if (candidates.length > 1) {
      console.debug(`MithyaX: ${candidates.length} remote video tiles detected; analyzing only the largest.`);
    }
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

  // isCurrentVideoElVerifiedRemote re-checks, right before a frame is
  // actually captured, that currentVideoEl's track still matches
  // isRemoteTrack. Structurally redundant today — currentVideoEl is
  // only ever assigned from findRemoteVideoElement's already-filtered
  // result — but this is the one invariant on this whole page that must
  // never regress silently: if a future change ever lets a
  // non-remote-verified element reach this point, capturing and
  // streaming it would mean analyzing the user's own camera. Cheap
  // enough to check on every frame; the alternative (trusting the
  // earlier filter forever) isn't worth the risk.
  function isCurrentVideoElVerifiedRemote() {
    const stream = currentVideoEl?.srcObject;
    if (!(stream instanceof MediaStream)) return false;
    const [track] = stream.getVideoTracks();
    return isRemoteTrack(track);
  }

  async function sampleFrame() {
    if (sampling || !currentVideoEl || !port) return;
    if (!isCurrentVideoElVerifiedRemote()) {
      console.error("MithyaX: currentVideoEl is no longer verified remote — stopping rather than risk capturing a local camera.");
      handleFatal(new Error("lost remote-track verification for currentVideoEl"));
      return;
    }
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
    missedVideoTicks = 0;
    clearInterval(frameTimer);
    frameTimer = null;
    clearInterval(positionTimer);
    positionTimer = null;
    if (audioCapture) {
      audioCapture.stop();
      audioCapture = null;
    }
    audioStreamRef = null;
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

  // refreshAudioCapture re-checks the remote audio stream every tick
  // (while a session is running) rather than only once — so a replaced
  // <audio> element, or the remote audio disappearing/reappearing
  // independently of video, is actually noticed. Comparing by stream
  // identity (===) means a genuinely unchanged stream is left alone
  // (RemoteAudioCapture isn't torn down and restarted every 2s for no
  // reason); muting doesn't change srcObject at all, so a muted
  // participant's stream keeps comparing equal and capture just carries
  // on processing silence — audio_result naturally reflects that, no
  // special-casing needed here.
  function refreshAudioCapture() {
    const stream = findRemoteAudioStream();

    if (!stream) {
      if (audioCapture) {
        audioCapture.stop();
        audioCapture = null;
        audioStreamRef = null;
      }
      return;
    }

    if (stream === audioStreamRef) return;

    if (audioCapture) audioCapture.stop();
    audioStreamRef = stream;
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

  function tick() {
    if (killed) return;
    const videoEl = findRemoteVideoElement();

    if (videoEl) {
      missedVideoTicks = 0;
      currentVideoEl = videoEl;
      if (!started) startCapture();
    } else if (started) {
      // Not found this tick — could be a genuine "left," or could be a
      // transient DOM swap/camera blip (see this file's header doc).
      // Stop sampling frames from a stale/possibly-detached element
      // (sampleFrame's own !currentVideoEl guard handles that) but keep
      // the session itself alive until the miss has persisted for
      // PARTICIPANT_LEFT_GRACE_TICKS ticks in a row.
      currentVideoEl = null;
      missedVideoTicks++;
      if (missedVideoTicks >= PARTICIPANT_LEFT_GRACE_TICKS) {
        stopCapture();
        return;
      }
    }

    if (started) refreshAudioCapture();
  }

  setInterval(tick, DETECT_INTERVAL_MS);
  tick();
})();
