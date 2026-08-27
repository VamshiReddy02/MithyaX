from __future__ import annotations

import io
import wave

import numpy as np
import pytest

from app.audio.decoder import InvalidAudioError, decode_audio


def make_wav_bytes(
    *,
    channels: int = 1,
    sample_rate: int = 16000,
    sample_width: int = 2,
    num_frames: int = 16000,
) -> bytes:
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as wav_file:
        wav_file.setnchannels(channels)
        wav_file.setsampwidth(sample_width)
        wav_file.setframerate(sample_rate)
        wav_file.writeframes(b"\x00" * (sample_width * channels * num_frames))
    return buffer.getvalue()


def test_load_valid_wav():
    data = make_wav_bytes(num_frames=16000)

    decoded = decode_audio(data)

    assert isinstance(decoded.waveform, np.ndarray)
    assert len(decoded.waveform) == 16000
    assert decoded.waveform.dtype == np.int16


def test_audio_metadata():
    data = make_wav_bytes(channels=1, sample_rate=16000, num_frames=32000)

    decoded = decode_audio(data)

    assert decoded.sample_rate == 16000
    assert decoded.channels == 1
    assert decoded.duration == pytest.approx(2.0)


def test_stereo_wav_reshapes_waveform_per_channel():
    data = make_wav_bytes(channels=2, sample_rate=16000, num_frames=8000)

    decoded = decode_audio(data)

    assert decoded.channels == 2
    assert decoded.waveform.shape == (8000, 2)


@pytest.mark.parametrize(
    "data",
    [
        pytest.param(b"", id="empty"),
        pytest.param(b"this is not a wav file at all", id="garbage-bytes"),
        pytest.param(b"RIFF" + b"\x00" * 4 + b"WAVEjunk", id="truncated-riff-header"),
    ],
)
def test_invalid_audio(data):
    with pytest.raises(InvalidAudioError):
        decode_audio(data)


def test_invalid_audio_zero_frames():
    data = make_wav_bytes(num_frames=0)

    with pytest.raises(InvalidAudioError):
        decode_audio(data)


def test_invalid_audio_unsupported_sample_width():
    # wave.py only accepts 1/2/3/4-byte sample widths at write time, so we
    # exercise the rejection path directly through the byte-level check
    # rather than trying to coax wave.Wave_write into producing one.
    data = bytearray(make_wav_bytes(sample_width=2, num_frames=100))

    # The "fmt " chunk's bitsPerSample field (offset 34) controls sample
    # width; corrupt it to an unsupported value (24-bit / 3 bytes).
    data[34] = 24

    with pytest.raises(InvalidAudioError):
        decode_audio(bytes(data))
