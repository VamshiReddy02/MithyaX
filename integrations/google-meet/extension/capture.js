// capture.js — turns a remote <video>/<audio> element into the wire
// formats the gateway expects. Runs in content.js's isolated world
// (service workers have no canvas/AudioContext, so this can't live in
// background.js). Frame capture and WAV encoding are ported verbatim
// from web/live-session.js's captureFrame/encodeWav — same technique,
// pointed at a remote participant's tile instead of the local camera.
(function () {
  const FRAME_JPEG_QUALITY = 0.8;

  function captureFrame(videoEl) {
    if (!videoEl.videoWidth || !videoEl.videoHeight) return Promise.resolve(null);
    const canvas = document.createElement("canvas");
    canvas.width = videoEl.videoWidth;
    canvas.height = videoEl.videoHeight;
    canvas.getContext("2d").drawImage(videoEl, 0, 0, canvas.width, canvas.height);
    return new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", FRAME_JPEG_QUALITY));
  }

  function blobToBase64(blob) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onloadend = () => resolve(reader.result.split(",")[1]);
      reader.onerror = reject;
      reader.readAsDataURL(blob);
    });
  }

  async function captureFrameBase64(videoEl) {
    const blob = await captureFrame(videoEl);
    return blob ? blobToBase64(blob) : null;
  }

  function writeAsciiString(view, offset, str) {
    for (let i = 0; i < str.length; i++) view.setUint8(offset + i, str.charCodeAt(i));
  }

  function encodeWav(samples, sampleRate) {
    const buffer = new ArrayBuffer(44 + samples.length * 2);
    const view = new DataView(buffer);

    writeAsciiString(view, 0, "RIFF");
    view.setUint32(4, 36 + samples.length * 2, true);
    writeAsciiString(view, 8, "WAVE");
    writeAsciiString(view, 12, "fmt ");
    view.setUint32(16, 16, true);
    view.setUint16(20, 1, true);
    view.setUint16(22, 1, true);
    view.setUint32(24, sampleRate, true);
    view.setUint32(28, sampleRate * 2, true);
    view.setUint16(32, 2, true);
    view.setUint16(34, 16, true);
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
    const total = chunks.reduce((sum, c) => sum + c.length, 0);
    const merged = new Float32Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      merged.set(chunk, offset);
      offset += chunk.length;
    }
    return merged;
  }

  // Buffers a remote MediaStream's audio and hands back ~chunkSeconds
  // WAV blobs via onChunk. Mirrors web/live-session.js's
  // startAudioCapture/flushAudioChunk.
  class RemoteAudioCapture {
    constructor(stream, { chunkSeconds = 3, onChunk } = {}) {
      this.chunkSeconds = chunkSeconds;
      this.onChunk = onChunk || (() => {});
      this.pending = [];

      this.audioContext = new (window.AudioContext || window.webkitAudioContext)();
      this.sourceNode = this.audioContext.createMediaStreamSource(stream);
      this.processorNode = this.audioContext.createScriptProcessor(4096, 1, 1);
      this.silentGain = this.audioContext.createGain();
      this.silentGain.gain.value = 0;

      this.processorNode.onaudioprocess = (event) => {
        this.pending.push(new Float32Array(event.inputBuffer.getChannelData(0)));
        const total = this.pending.reduce((sum, c) => sum + c.length, 0);
        if (total >= this.audioContext.sampleRate * this.chunkSeconds) this.flush();
      };

      // Route through a zero-gain node so the remote audio isn't
      // doubled up on playback — Meet already plays it through its own
      // <audio>/<video> element.
      this.sourceNode.connect(this.processorNode);
      this.processorNode.connect(this.silentGain);
      this.silentGain.connect(this.audioContext.destination);
    }

    async flush() {
      if (this.pending.length === 0) return;
      const samples = mergeFloat32(this.pending);
      this.pending = [];
      const wavBlob = encodeWav(samples, this.audioContext.sampleRate);
      const data = await blobToBase64(wavBlob);
      this.onChunk(data);
    }

    stop() {
      this.processorNode.disconnect();
      this.processorNode.onaudioprocess = null;
      this.sourceNode.disconnect();
      this.silentGain.disconnect();
      this.audioContext.close();
      this.pending = [];
    }
  }

  window.__mithyax = window.__mithyax || {};
  window.__mithyax.capture = { captureFrameBase64, RemoteAudioCapture };
})();
