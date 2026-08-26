from pathlib import Path

from fastapi.testclient import TestClient

from app.server import app

client = TestClient(app)

SAMPLE_WAV = Path(__file__).resolve().parent.parent / "samples" / "test.wav"


def test_health():
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_analyze_audio_returns_metadata():
    with SAMPLE_WAV.open("rb") as f:
        response = client.post(
            "/analyze-audio",
            files={"audio": ("test.wav", f, "audio/wav")},
        )

    assert response.status_code == 200
    assert response.json() == {
        "audio": "test.wav",
        "duration": 1.0,
        "sample_rate": 16000,
        "channels": 1,
    }


def test_analyze_audio_rejects_invalid_file():
    response = client.post(
        "/analyze-audio",
        files={"audio": ("not-audio.wav", b"this is not a wav file", "audio/wav")},
    )

    assert response.status_code == 422


def test_analyze_audio_requires_a_file():
    response = client.post("/analyze-audio")

    assert response.status_code == 422
