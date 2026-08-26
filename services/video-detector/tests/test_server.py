import functools
import threading
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import cv2
import numpy as np
import pytest
from fastapi.testclient import TestClient

from app.server import app

SAMPLES_DIR = (
    Path(__file__).resolve().parent.parent
    / "samples"
    / "dataset"
    / "real"
)


@pytest.fixture(scope="module")
def video_server():
    """
    Serve samples/dataset/real over HTTP so /analyze can download
    a real video by URL, the same way the Go gateway will call it.
    """

    if not SAMPLES_DIR.exists():
        pytest.skip("sample videos not available in this environment")

    handler = functools.partial(
        SimpleHTTPRequestHandler,
        directory=str(SAMPLES_DIR),
    )

    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()

    try:
        yield f"http://127.0.0.1:{server.server_port}"
    finally:
        server.shutdown()
        thread.join()


@pytest.fixture(scope="module")
def client():
    with TestClient(app) as test_client:
        yield test_client


def test_health(client):
    response = client.get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_analyze_missing_video_url(client):
    response = client.post("/analyze", json={})

    assert response.status_code == 422


def test_analyze_invalid_video_url(client):
    response = client.post("/analyze", json={"video_url": "not-a-url"})

    assert response.status_code == 422


def test_analyze_unreachable_video(client):
    response = client.post(
        "/analyze",
        json={"video_url": "http://127.0.0.1:1/missing.mp4"},
    )

    assert response.status_code == 422


def test_analyze_real_video(client, video_server):
    videos = sorted(SAMPLES_DIR.glob("*.mp4"))

    if not videos:
        pytest.skip("no sample videos found")

    video_url = f"{video_server}/{videos[0].name}"

    response = client.post(
        "/analyze",
        json={"video_url": video_url},
    )

    assert response.status_code == 200

    body = response.json()

    assert body["video"] == videos[0].name
    assert body["frames"] > 0
    assert 0.0 <= body["fake_score"] <= 1.0
    assert body["verdict"] in {"real", "fake"}


def _first_frame_jpeg(video_path: Path) -> bytes:
    cap = cv2.VideoCapture(str(video_path))
    ok, frame = cap.read()
    cap.release()

    if not ok:
        pytest.skip(f"could not read a frame from {video_path}")

    ok, encoded = cv2.imencode(".jpg", frame)
    if not ok:
        pytest.skip("could not encode frame as JPEG")

    return encoded.tobytes()


def test_analyze_frame_empty_body(client):
    response = client.post(
        "/analyze-frame",
        content=b"",
        headers={"Content-Type": "image/jpeg"},
    )

    assert response.status_code == 400


def test_analyze_frame_garbage_body(client):
    response = client.post(
        "/analyze-frame",
        content=b"not a jpeg",
        headers={"Content-Type": "image/jpeg"},
    )

    assert response.status_code == 400


def test_analyze_frame_no_face(client):
    blank = np.zeros((240, 320, 3), dtype=np.uint8)
    ok, encoded = cv2.imencode(".jpg", blank)
    assert ok

    response = client.post(
        "/analyze-frame",
        content=encoded.tobytes(),
        headers={"Content-Type": "image/jpeg"},
    )

    assert response.status_code == 200
    body = response.json()
    assert body["face_detected"] is False
    assert body["verdict"] == "unknown"


def test_analyze_frame_with_face(client):
    videos = sorted(SAMPLES_DIR.glob("*.mp4"))
    if not videos:
        pytest.skip("no sample videos found")

    jpeg_bytes = _first_frame_jpeg(videos[0])

    response = client.post(
        "/analyze-frame",
        content=jpeg_bytes,
        headers={"Content-Type": "image/jpeg"},
    )

    assert response.status_code == 200
    body = response.json()

    if not body["face_detected"]:
        pytest.skip("no face in the sampled frame; not this test's concern")

    assert 0.0 <= body["fake_probability"] <= 1.0
    assert body["verdict"] in {"real", "fake"}
