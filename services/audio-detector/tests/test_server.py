from fastapi.testclient import TestClient

from app.server import app

client = TestClient(app)


def test_health():
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_analyze_audio_returns_mock_response():
    response = client.post(
        "/analyze-audio",
        files={"audio": ("sample.wav", b"fake-audio-bytes", "audio/wav")},
    )

    assert response.status_code == 200

    body = response.json()
    assert body == {
        "audio": "sample.wav",
        "duration": 0,
        "chunks_analyzed": 0,
        "fake_score": 0,
        "verdict": "real",
    }


def test_analyze_audio_requires_a_file():
    response = client.post("/analyze-audio")

    assert response.status_code == 422
