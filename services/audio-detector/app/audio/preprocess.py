"""Audio preprocessing: mono conversion, resampling, normalization, and
chunking, ahead of feature extraction and detection.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from app.audio.decoder import DecodedAudio
from app.config import CHUNK_SECONDS, TARGET_SAMPLE_RATE


@dataclass
class PreprocessedAudio:
    waveform: np.ndarray  # mono, float32, peak-normalized, at TARGET_SAMPLE_RATE
    sample_rate: int
    duration: float
    chunks: list[np.ndarray]


def to_mono(waveform: np.ndarray) -> np.ndarray:
    """Downmix a multi-channel waveform to mono by averaging channels.

    A 1-D (already mono) input is returned as float32, unchanged in shape.
    """
    if waveform.ndim == 1:
        return waveform.astype(np.float32)
    return waveform.astype(np.float32).mean(axis=1)


def resample(
    waveform: np.ndarray,
    orig_sample_rate: int,
    target_sample_rate: int = TARGET_SAMPLE_RATE,
) -> np.ndarray:
    """Resample a mono waveform to target_sample_rate via linear interpolation.

    Good enough for voice-detection preprocessing without pulling in a
    dedicated resampling library (librosa/soxr) at this stage.
    """
    if orig_sample_rate == target_sample_rate:
        return waveform.astype(np.float32)
    if waveform.size == 0:
        return waveform.astype(np.float32)

    duration = len(waveform) / float(orig_sample_rate)
    target_length = max(1, round(duration * target_sample_rate))

    original_times = np.linspace(0.0, duration, num=len(waveform), endpoint=False)
    target_times = np.linspace(0.0, duration, num=target_length, endpoint=False)

    return np.interp(target_times, original_times, waveform).astype(np.float32)


def normalize(waveform: np.ndarray) -> np.ndarray:
    """Peak-normalize waveform to [-1, 1].

    Silent (all-zero) input is returned unchanged rather than divided by
    zero.
    """
    peak = np.max(np.abs(waveform)) if waveform.size else 0.0
    if peak == 0:
        return waveform.astype(np.float32)
    return (waveform / peak).astype(np.float32)


def chunk(
    waveform: np.ndarray,
    sample_rate: int,
    chunk_seconds: float = CHUNK_SECONDS,
) -> list[np.ndarray]:
    """Split waveform into consecutive chunk_seconds windows.

    The final chunk may be shorter than chunk_seconds if the waveform
    doesn't divide evenly — a shorter-than-one-window clip still yields
    exactly one (short) chunk. Empty input yields zero chunks.
    """
    if waveform.size == 0:
        return []

    chunk_length = max(1, int(round(chunk_seconds * sample_rate)))
    return [waveform[start : start + chunk_length] for start in range(0, len(waveform), chunk_length)]


def preprocess(decoded: DecodedAudio) -> PreprocessedAudio:
    """Run the full mono -> resample -> normalize -> chunk pipeline."""
    mono = to_mono(decoded.waveform)
    resampled = resample(mono, decoded.sample_rate, TARGET_SAMPLE_RATE)
    normalized = normalize(resampled)
    chunks = chunk(normalized, TARGET_SAMPLE_RATE)
    duration = len(normalized) / float(TARGET_SAMPLE_RATE)

    return PreprocessedAudio(
        waveform=normalized,
        sample_rate=TARGET_SAMPLE_RATE,
        duration=duration,
        chunks=chunks,
    )
