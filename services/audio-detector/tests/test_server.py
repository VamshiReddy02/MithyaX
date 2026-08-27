from __future__ import annotations

import io
import wave
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.server import app

SAMPLE_WAV = Path(__file__).resolve().parent.parent / "samples" / "test.wav"


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


@pytest.fixture(scope="module")
def client():
    # The app loads the real detection model on startup (lifespan), so
    # the TestClient must be used as a context manager for that to run.
    with TestClient(app) as test_client:
        yield test_client


def test_health(client):
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_analyze_audio_returns_processed_metadata(client):
    with SAMPLE_WAV.open("rb") as f:
        response = client.post(
            "/analyze-audio",
            files={"audio": ("test.wav", f, "audio/wav")},
        )

    assert response.status_code == 200
    body = response.json()
    assert body["duration_seconds"] == 1.0
    assert body["sample_rate"] == 16000
    assert body["channels"] == 1
    assert body["chunks"] == 1
    assert body["status"] == "processed"
    assert 0.0 <= body["fake_score"] <= 1.0
    assert body["verdict"] in ("real", "fake")


def test_analyze_audio_downmixes_stereo_and_resamples(client):
    # 2 seconds, stereo, 8kHz — exercises mono conversion and resampling
    # through the real HTTP path, not just the preprocess unit tests.
    data = make_wav_bytes(channels=2, sample_rate=8000, num_frames=16000)

    response = client.post(
        "/analyze-audio",
        files={"audio": ("stereo.wav", data, "audio/wav")},
    )

    assert response.status_code == 200
    body = response.json()
    assert body["sample_rate"] == 16000
    assert body["channels"] == 1
    assert body["duration_seconds"] == pytest.approx(2.0)
    assert body["chunks"] == 1  # 2s < the 4s chunk window
    assert 0.0 <= body["fake_score"] <= 1.0


def test_analyze_audio_long_clip_produces_multiple_chunks(client):
    # ~12.4s @ 16kHz mono, matching the target response example.
    data = make_wav_bytes(sample_rate=16000, num_frames=int(12.4 * 16000))

    response = client.post(
        "/analyze-audio",
        files={"audio": ("long.wav", data, "audio/wav")},
    )

    assert response.status_code == 200
    body = response.json()
    assert body["duration_seconds"] == pytest.approx(12.4)
    assert body["chunks"] == 4
    assert 0.0 <= body["fake_score"] <= 1.0


def test_analyze_audio_rejects_invalid_file(client):
    response = client.post(
        "/analyze-audio",
        files={"audio": ("not-audio.wav", b"this is not a wav file", "audio/wav")},
    )

    assert response.status_code == 422


def test_analyze_audio_requires_a_file(client):
    response = client.post("/analyze-audio")

    assert response.status_code == 422
