from __future__ import annotations

import numpy as np
import pytest

from app.audio.decoder import DecodedAudio
from app.audio.preprocess import chunk, normalize, preprocess, resample, to_mono

SAMPLE_RATE = 16000


# --- to_mono ---


def test_to_mono_averages_channels():
    stereo = np.array([[100, 200], [300, 400]], dtype=np.int16)

    mono = to_mono(stereo)

    assert mono.dtype == np.float32
    np.testing.assert_allclose(mono, [150.0, 350.0])


def test_to_mono_passthrough_for_already_mono():
    mono_in = np.array([1, 2, 3], dtype=np.int16)

    mono_out = to_mono(mono_in)

    assert mono_out.dtype == np.float32
    np.testing.assert_allclose(mono_out, [1.0, 2.0, 3.0])


# --- resample ---


def test_resample_upsamples_to_target_rate():
    half_second = np.zeros(4000, dtype=np.float32)  # 0.5s @ 8000Hz

    resampled = resample(half_second, orig_sample_rate=8000, target_sample_rate=16000)

    assert len(resampled) == 8000  # 0.5s @ 16000Hz


def test_resample_downsamples_to_target_rate():
    one_second = np.zeros(16000, dtype=np.float32)  # 1s @ 16000Hz

    resampled = resample(one_second, orig_sample_rate=16000, target_sample_rate=8000)

    assert len(resampled) == 8000  # 1s @ 8000Hz


def test_resample_is_a_noop_when_rate_already_matches():
    waveform = np.array([1.0, 2.0, 3.0], dtype=np.float32)

    resampled = resample(waveform, orig_sample_rate=16000, target_sample_rate=16000)

    np.testing.assert_allclose(resampled, waveform)


# --- normalize ---


def test_normalize_scales_to_unit_peak():
    waveform = np.array([0.0, 2.0, -4.0, 1.0], dtype=np.float32)

    normalized = normalize(waveform)

    assert np.max(np.abs(normalized)) == pytest.approx(1.0)
    np.testing.assert_allclose(normalized, waveform / 4.0)


def test_normalize_handles_silence_without_dividing_by_zero():
    waveform = np.zeros(10, dtype=np.float32)

    normalized = normalize(waveform)

    np.testing.assert_allclose(normalized, waveform)


def test_normalize_scales_int16_range_waveform():
    waveform = np.array([16384, -32768, 100], dtype=np.int16).astype(np.float32)

    normalized = normalize(waveform)

    assert np.max(np.abs(normalized)) == pytest.approx(1.0)


# --- chunk: short / long audio ---


def test_chunk_short_audio_yields_one_partial_chunk():
    one_second = np.zeros(SAMPLE_RATE, dtype=np.float32)  # 1s, shorter than the 4s window

    chunks = chunk(one_second, sample_rate=SAMPLE_RATE, chunk_seconds=4.0)

    assert len(chunks) == 1
    assert len(chunks[0]) == SAMPLE_RATE


def test_chunk_long_audio_splits_into_multiple_windows():
    # 12.4s @ 16kHz, 4s windows -> ceil(12.4 / 4) = 4 chunks, matching the
    # target response example (duration_seconds: 12.4 -> chunks: 4).
    waveform = np.zeros(int(12.4 * SAMPLE_RATE), dtype=np.float32)

    chunks = chunk(waveform, sample_rate=SAMPLE_RATE, chunk_seconds=4.0)

    assert len(chunks) == 4
    for c in chunks[:-1]:
        assert len(c) == int(4.0 * SAMPLE_RATE)
    assert len(chunks[-1]) == len(waveform) - 3 * int(4.0 * SAMPLE_RATE)


def test_chunk_exact_multiple_of_window():
    eight_seconds = np.zeros(SAMPLE_RATE * 8, dtype=np.float32)

    chunks = chunk(eight_seconds, sample_rate=SAMPLE_RATE, chunk_seconds=4.0)

    assert len(chunks) == 2
    assert all(len(c) == SAMPLE_RATE * 4 for c in chunks)


def test_chunk_empty_waveform_yields_no_chunks():
    chunks = chunk(np.array([], dtype=np.float32), sample_rate=SAMPLE_RATE)

    assert chunks == []


# --- preprocess: full pipeline ---


def test_preprocess_full_pipeline():
    stereo_at_8k = np.zeros((8000, 2), dtype=np.int16)  # 1s @ 8000Hz, stereo
    stereo_at_8k[:, 0] = 1000
    stereo_at_8k[:, 1] = -1000
    decoded = DecodedAudio(waveform=stereo_at_8k, sample_rate=8000, channels=2, duration=1.0)

    processed = preprocess(decoded)

    assert processed.sample_rate == SAMPLE_RATE
    assert processed.waveform.ndim == 1  # downmixed to mono
    assert processed.duration == pytest.approx(1.0, abs=0.01)
    assert len(processed.chunks) == 1  # 1s of audio is shorter than the 4s window


def test_preprocess_long_audio_produces_multiple_chunks():
    waveform = np.random.default_rng(0).integers(-30000, 30000, size=16000 * 9).astype(np.int16)
    decoded = DecodedAudio(waveform=waveform, sample_rate=16000, channels=1, duration=9.0)

    processed = preprocess(decoded)

    assert len(processed.chunks) == 3  # ceil(9 / 4)
    assert np.max(np.abs(processed.waveform)) <= 1.0 + 1e-6
