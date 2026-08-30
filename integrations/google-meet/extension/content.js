// content.js — orchestrator. Runs inside the Meet tab, polls the DOM for
// every remote participant's media elements, and drives capture.js
// (frame + audio capture) and ui.js (one badge per participant) through
// an independent long-lived port per participant to background.js,
// which owns each participant's actual MithyaX session.
//
// Phase 8.4: this used to track exactly one participant (the largest
// on-screen tile) via a handful of module-level scalars. It's now a
// Map of participants, keyed by their video MediaStreamTrack.id — a
// spec-guaranteed stable id for that track's lifetime, unaffected by
// which DOM element currently renders it (the same property 8.3's
// element-replacement handling already relied on for the single-
// participant case). background.js needed zero changes for this: its
// session/state/error handling was already scoped per
// chrome.runtime.connect port inside its onConnect closure, not a
// module-level singleton — so opening one port per participant here
// already gives each one a fully independent MithyaXSession, detector
// state, and error handling for free.
//
// Phase 8.3 hardening carries over conceptually unchanged, just
// re-scoped from module-level scalars to per-participant state: a
// missed detection doesn't instantly end a participant's session
// (PARTICIPANT_LEFT_GRACE_TICKS), the remote audio stream is re-checked
// every tick rather than once, and a frame is never captured from an
// element that doesn't currently verify as remote.
(function () {
  const FRAME_SAMPLE_INTERVAL_MS = 1000;
  const AUDIO_CHUNK_SECONDS = 3;
  const DETECT_INTERVAL_MS = 2000;
  const POSITION_INTERVAL_MS = 500;
  // ~6s at DETECT_INTERVAL_MS=2000ms: long enough to ride out a DOM
  // element swap or a several-second camera-off blip without losing a
  // participant's session continuity (a fresh session means a fresh
  // WebSocket and a reset temporal/risk state), short enough that an
  // actual "left" is still noticed within a few seconds.
  const PARTICIPANT_LEFT_GRACE_TICKS = 3;
  // Bounds how many participants this tab analyzes at once — independent
  // of the gateway's own, much larger RealtimeMaxSessions cap (process-
  // wide, enforced server-side). Each participant here is a full
  // WebSocket + AudioContext + 1fps JPEG capture loop; this protects the
  // browser tab's own CPU/bandwidth in a large call, not the gateway.
  // Already-tracked participants are never evicted to make room for a
  // new one — the cap only gates whether a brand-new tile gets a slot.
  const MAX_CONCURRENT_PARTICIPANTS = 4;

  const { captureFrameBase64, RemoteAudioCapture } = window.__mithyax.capture;
  const { updateBadge, removeBadge, positionBadge } = window.__mithyax.ui;

  // One entry per remote participant currently being analyzed, keyed by
  // their video track's id. See the Participant shape in
  // startParticipant — this replaces every module-level scalar the
  // single-participant version of this file used to have.
  const participants = new Map();

  let positionTimer = null;
  let killed = false;

  // Stops everything permanently for the rest of this tab's lifetime —
  // for anything with no recovery short of the user reloading the tab.
  // This is a whole-tab condition (the extension's runtime itself is
  // gone), not a per-participant one — unlike a single participant's
  // session erroring or ending, which only tears down that one entry
  // (see stopParticipant).
  function handleFatal(err) {
    if (killed) return;
    killed = true;
    console.warn("MithyaX: stopping — reload this tab to reconnect.", err);
    for (const key of [...participants.keys()]) stopParticipant(key);
    clearInterval(positionTimer);
    positionTimer = null;
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
  // multi-party call since 8.3's tightening — do that before relying on
  // it for anything beyond this proof-of-concept.
  function isRemoteTrack(track) {
    return !!track && /^remote/i.test(track.label);
  }

  // findRemoteVideoElements returns every currently-valid remote video
  // tile, keyed by that tile's video track id (see the file header for
  // why that id is the stable per-participant key). Unlike 8.3's
  // findRemoteVideoElement, this doesn't pick a single "largest" one —
  // every remote participant is a candidate; startParticipant's own
  // caller (tick) is what applies MAX_CONCURRENT_PARTICIPANTS.
  function findRemoteVideoElements() {
    const found = new Map(); // track id -> el
    for (const el of document.querySelectorAll("video")) {
      const stream = el.srcObject;
      if (!(stream instanceof MediaStream)) continue;
      const [track] = stream.getVideoTracks();
      if (!isRemoteTrack(track) || el.videoWidth <= 0) continue;
      found.set(track.id, el);
    }
    return found;
  }

  // findRemoteAudioStreams returns every remote-audio-track-carrying
  // MediaStream discoverable anywhere in the DOM (both <audio> and
  // <video> elements — a stream can be attached to either), deduplicated
  // by stream identity so one stream reachable through two elements only
  // counts once.
  function findRemoteAudioStreams() {
    const streams = new Set();
    for (const el of [...document.querySelectorAll("audio"), ...document.querySelectorAll("video")]) {
      const stream = el.srcObject;
      if (!(stream instanceof MediaStream)) continue;
      const [track] = stream.getAudioTracks();
      if (isRemoteTrack(track)) streams.add(stream);
    }
    return streams;
  }

  // isVerifiedRemoteVideo re-checks, right before a frame is actually
  // captured, that el's track still matches isRemoteTrack. Structurally
  // redundant today — el only ever comes from findRemoteVideoElements'
  // already-filtered result — but this is the one invariant on this
  // whole page that must never regress silently: if a future change
  // ever lets a non-remote-verified element reach this point, capturing
  // and streaming it would mean analyzing the user's own camera. Cheap
  // enough to check on every frame; the alternative (trusting the
  // earlier filter forever) isn't worth the risk.
  function isVerifiedRemoteVideo(el) {
    const stream = el?.srcObject;
    if (!(stream instanceof MediaStream)) return false;
    const [track] = stream.getVideoTracks();
    return isRemoteTrack(track);
  }

  function connectPort(key) {
    let port;
    try {
      port = chrome.runtime.connect({ name: "mithyax" });
    } catch (err) {
      handleFatal(err);
      return null;
    }
    port.onMessage.addListener((message) => {
      switch (message.type) {
        case "state":
          updateBadge(key, message.state);
          break;
        // "error"/"closed" used to be silently dropped here — background.js
        // still forwards them (see its own safePost calls), but nothing
        // ever surfaced them: the badge stayed frozen wherever it last
        // was (e.g. "Analyzing" forever) with no visible sign anything
        // had gone wrong. A real cause is a plain fetch/WebSocket
        // failure — a gateway that isn't reachable, an unedited
        // GATEWAY_EXTENSION_TOKEN placeholder, credential exchange
        // failing — logged here (and by background.js itself, see its
        // own console.error) so the failure is at least visible in one
        // of the two consoles. Phase 8.4: also tears this one
        // participant down (not stuck showing "Error" forever while
        // everyone else keeps working) so the next tick's fresh
        // detection can retry it — full reconnect-of-an-active-session
        // logic is still out of scope, same as every prior phase.
        case "error":
          console.error(`MithyaX[${key}]: session error —`, message.message);
          updateBadge(key, { tier: "unknown", label: "Error — see console" });
          stopParticipant(key);
          break;
        case "closed":
          console.warn(`MithyaX[${key}]: gateway session closed.`);
          updateBadge(key, { tier: "unknown", label: "Disconnected" });
          stopParticipant(key);
          break;
      }
    });
    port.onDisconnect.addListener(() => {
      // Full cleanup (badge removed, audio capture stopped), not just a
      // flag reset — this fires whenever background.js's service worker
      // is recycled or reloaded mid-session (normal MV3 lifecycle, not
      // just tab close/navigation). Only this one participant's entry is
      // affected; every other tracked participant is untouched.
      stopParticipant(key);
    });
    return port;
  }

  async function sampleFrame(p) {
    if (p.sampling || !p.currentVideoEl || !p.port) return;
    if (!isVerifiedRemoteVideo(p.currentVideoEl)) {
      // Skip only *this* frame — do not treat this as fatal. A remote
      // participant turning their camera off commonly removes the video
      // track while leaving the element itself in place (so
      // currentVideoEl isn't null, but its track no longer verifies as
      // remote); Meet can also transiently reuse a tile's element across
      // participants. Neither is a security problem by itself — the
      // invariant this check protects (never actually send a frame from
      // a non-remote track) is already satisfied by returning here.
      // Whether this is a real "participant left" is tick()'s call to
      // make, via its own grace-period logic — this function has no
      // business permanently killing a session over what's usually a
      // normal, temporary state change. (An earlier, single-participant
      // version of this check called handleFatal here, which meant a
      // camera-off/on toggle could permanently end the session until the
      // tab was reloaded — confirmed live; don't reintroduce that.)
      return;
    }
    p.sampling = true;
    try {
      const data = await captureFrameBase64(p.currentVideoEl);
      if (data) p.port.postMessage({ type: "frame", data });
    } catch (err) {
      handleFatal(err);
    } finally {
      p.sampling = false;
    }
  }

  // startParticipant begins tracking a newly-detected remote participant:
  // opens their own port to background.js (which gives them a fully
  // independent MithyaXSession — see the file header), and adds their
  // entry to participants. Called only from tick(), which has already
  // checked MAX_CONCURRENT_PARTICIPANTS.
  function startParticipant(key, videoEl) {
    if (killed || participants.has(key)) return;
    const port = connectPort(key);
    if (!port) return;

    const p = {
      key,
      port,
      currentVideoEl: videoEl,
      missedTicks: 0,
      sampling: false,
      audioCapture: null,
      audioStreamRef: null,
    };
    participants.set(key, p);

    try {
      port.postMessage({ type: "start" });
    } catch (err) {
      handleFatal(err);
      return;
    }
    updateBadge(key, { tier: "unknown", label: "Analyzing" });
    positionBadge(key, videoEl);
  }

  // stopParticipant tears down exactly one participant's entry — their
  // port, audio capture, and badge — leaving every other tracked
  // participant completely unaffected. This is what makes "one
  // participant leaving/rejoining must not affect the others" true: it
  // falls out of every piece of state living in that one Map entry,
  // rather than being module-level and shared.
  function stopParticipant(key) {
    const p = participants.get(key);
    if (!p) return;
    participants.delete(key);

    if (p.audioCapture) p.audioCapture.stop();

    if (p.port) {
      try {
        p.port.postMessage({ type: "stop" });
        p.port.disconnect();
      } catch {
        // already gone — nothing to clean up
      }
    }
    removeBadge(key);
  }

  // refreshAudioPairing re-derives, every tick, which audio stream (if
  // any) belongs to each tracked participant, and starts/stops each
  // one's RemoteAudioCapture as that pairing changes. See the file
  // header's design doc for the two-pass strategy this implements:
  // each participant's own video stream is checked first (the common
  // case — one combined MediaStream per participant), and only when
  // there's exactly one participant still needing audio and exactly one
  // unclaimed audio stream elsewhere in the DOM does it fall back to
  // pairing them — the same single-audio/single-video shape 8.3's live
  // test already proved works. With two or more of either left
  // unclaimed, pairing is genuinely ambiguous from the DOM alone, so
  // nothing is guessed — those participants simply go without audio
  // until the ambiguity resolves itself (e.g. down to one).
  function refreshAudioPairing() {
    const claimed = new Set();
    const pending = new Map(); // key -> stream, or key -> null if still needed

    for (const [key, p] of participants) {
      const stream = p.currentVideoEl?.srcObject;
      if (stream instanceof MediaStream) {
        const [track] = stream.getAudioTracks();
        if (isRemoteTrack(track)) {
          pending.set(key, stream);
          claimed.add(stream);
          continue;
        }
      }
      pending.set(key, null);
    }

    const needsAudio = [...pending.entries()].filter(([, stream]) => stream === null);
    const unclaimed = [...findRemoteAudioStreams()].filter((s) => !claimed.has(s));
    if (needsAudio.length === 1 && unclaimed.length === 1) {
      pending.set(needsAudio[0][0], unclaimed[0]);
    } else if (needsAudio.length > 1 && unclaimed.length > 1) {
      console.debug(
        `MithyaX: ${needsAudio.length} participants and ${unclaimed.length} unattributed remote audio streams — pairing is ambiguous, leaving them without audio rather than guessing.`,
      );
    }

    for (const [key, stream] of pending) {
      applyAudioStream(participants.get(key), stream);
    }
  }

  function applyAudioStream(p, stream) {
    if (!p) return;

    if (!stream) {
      if (p.audioCapture) {
        p.audioCapture.stop();
        p.audioCapture = null;
        p.audioStreamRef = null;
      }
      return;
    }

    if (stream === p.audioStreamRef) return;

    if (p.audioCapture) p.audioCapture.stop();
    p.audioStreamRef = stream;
    p.audioCapture = new RemoteAudioCapture(stream, {
      chunkSeconds: AUDIO_CHUNK_SECONDS,
      onChunk: (data) => {
        if (!p.port) return;
        try {
          p.port.postMessage({ type: "audio_chunk", data });
        } catch (err) {
          handleFatal(err);
        }
      },
    });
  }

  // tick reconciles the currently-tracked participants against what's
  // actually in the DOM right now: found again (reset its miss counter,
  // update its element reference — this is what makes a replaced <video>
  // element a no-op instead of a teardown), missing (grace-period logic,
  // unchanged from 8.3, now per-participant), or brand new (started,
  // subject to MAX_CONCURRENT_PARTICIPANTS).
  function tick() {
    if (killed) return;
    const found = findRemoteVideoElements();

    const toStop = [];
    for (const [key, p] of participants) {
      const el = found.get(key);
      if (el) {
        p.missedTicks = 0;
        p.currentVideoEl = el;
        continue;
      }
      // Not found this tick — could be a genuine "left," or could be a
      // transient DOM swap/camera blip (see the file header). Stop
      // sampling frames from a stale/possibly-detached element
      // (sampleFrame's own !currentVideoEl guard handles that) but keep
      // this participant's session alive until the miss has persisted
      // for PARTICIPANT_LEFT_GRACE_TICKS ticks in a row.
      p.currentVideoEl = null;
      p.missedTicks++;
      if (p.missedTicks >= PARTICIPANT_LEFT_GRACE_TICKS) toStop.push(key);
    }
    for (const key of toStop) stopParticipant(key);

    if (participants.size < MAX_CONCURRENT_PARTICIPANTS) {
      for (const [key, el] of found) {
        if (participants.size >= MAX_CONCURRENT_PARTICIPANTS) break;
        if (!participants.has(key)) startParticipant(key, el);
      }
    } else {
      const newTileCount = [...found.keys()].filter((key) => !participants.has(key)).length;
      if (newTileCount > 0) {
        console.debug(`MithyaX: at the ${MAX_CONCURRENT_PARTICIPANTS}-participant cap, not starting ${newTileCount} newly-detected tile(s).`);
      }
    }

    refreshAudioPairing();
  }

  setInterval(tick, DETECT_INTERVAL_MS);
  positionTimer = setInterval(() => {
    for (const p of participants.values()) positionBadge(p.key, p.currentVideoEl);
  }, POSITION_INTERVAL_MS);

  // One shared frame-sampling timer iterating every tracked participant,
  // rather than one timer per participant — simpler to reason about and
  // avoids N independent timers drifting relative to each other. Each
  // participant's own `sampling` flag still prevents its own capture
  // calls from overlapping if that one participant's capture is slow;
  // it has no effect on any other participant's sampling.
  setInterval(() => {
    for (const p of participants.values()) sampleFrame(p);
  }, FRAME_SAMPLE_INTERVAL_MS);

  tick();
})();
