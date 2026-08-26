"""Audio file loading and decoding.

WAV only for now — no MP3/M4A/WebM support yet. That's future scope, not
this step.
"""

from __future__ import annotations

import io
import wave
from dataclasses import dataclass

import numpy as np

# WAV sample widths (bytes) mapped to the numpy dtype that reinterprets
# the raw PCM frames correctly. 8-bit WAV samples are unsigned; 16- and
# 32-bit are signed — that's the WAV format's own convention, not a
# choice we're making.
_DTYPE_FOR_SAMPLE_WIDTH = {
    1: np.uint8,
    2: np.int16,
    4: np.int32,
}


class InvalidAudioError(ValueError):
    """Raised when uploaded audio can't be decoded as a valid WAV file."""


@dataclass
class DecodedAudio:
    waveform: np.ndarray
    sample_rate: int
    channels: int
    duration: float


def load_wav(data: bytes) -> DecodedAudio:
    """
    Decode raw WAV bytes into a waveform plus its metadata.

    Raises InvalidAudioError if data isn't a readable WAV file, has no
    audio frames, or uses a sample width we don't recognize.
    """
    try:
        with wave.open(io.BytesIO(data), "rb") as wav_file:
            channels = wav_file.getnchannels()
            sample_rate = wav_file.getframerate()
            sample_width = wav_file.getsampwidth()
            frame_count = wav_file.getnframes()
            raw_frames = wav_file.readframes(frame_count)
    except (wave.Error, EOFError) as exc:
        raise InvalidAudioError(f"could not decode WAV audio: {exc}") from exc

    if frame_count == 0 or not raw_frames:
        raise InvalidAudioError("WAV file contains no audio frames")

    if sample_rate <= 0:
        raise InvalidAudioError(f"invalid WAV sample rate: {sample_rate}")

    dtype = _DTYPE_FOR_SAMPLE_WIDTH.get(sample_width)
    if dtype is None:
        raise InvalidAudioError(f"unsupported WAV sample width: {sample_width} bytes")

    waveform = np.frombuffer(raw_frames, dtype=dtype)
    if channels > 1:
        waveform = waveform.reshape(-1, channels)

    duration = frame_count / float(sample_rate)

    return DecodedAudio(
        waveform=waveform,
        sample_rate=sample_rate,
        channels=channels,
        duration=duration,
    )
