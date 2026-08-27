import cv2
import numpy as np
import pytest

from app.diagnostic import (
    MAX_FRAME_METADATA,
    analyze_video,
    sample_frame_metadata,
)
from app.embedding import FaceEmbeddingModel
from app.face import FaceDetector
from app.model import DeepfakeDetector


def test_sample_frame_metadata_returns_everything_when_under_cap():
    entries = [{"timestamp": i} for i in range(10)]

    sampled = sample_frame_metadata(entries, max_samples=60)

    assert sampled == entries


def test_sample_frame_metadata_caps_and_keeps_endpoints():
    entries = [{"timestamp": i} for i in range(500)]

    sampled = sample_frame_metadata(entries, max_samples=60)

    assert len(sampled) <= 60
    assert sampled[0] == entries[0]
    assert sampled[-1] == entries[-1]

    # Chronological order survives downsampling.
    timestamps = [entry["timestamp"] for entry in sampled]
    assert timestamps == sorted(timestamps)


def test_sample_frame_metadata_default_matches_module_constant():
    entries = [{"timestamp": i} for i in range(1000)]

    sampled = sample_frame_metadata(entries)

    assert len(sampled) <= MAX_FRAME_METADATA


def _blank_video(tmp_path, num_frames=12, fps=10.0):
    video_path = tmp_path / "blank.mp4"

    fourcc = cv2.VideoWriter_fourcc(*"mp4v")
    writer = cv2.VideoWriter(str(video_path), fourcc, fps, (640, 480))

    if not writer.isOpened():
        pytest.skip("OpenCV cannot create MP4 videos in this environment")

    try:
        for _ in range(num_frames):
            frame = np.zeros((480, 640, 3), dtype=np.uint8)
            writer.write(frame)
    finally:
        writer.release()

    return video_path


def test_analyze_video_reports_frame_metadata_for_every_frame(tmp_path):
    """
    The internal analysis loop must still walk every frame — collecting
    FrameMetadata alongside is additive, not a replacement for it. This
    uses blank frames (no face anywhere) specifically so face_detected
    stays False throughout, proving frame_metadata covers frames with
    no detected face too, not just the ones that fed the aggregate
    score.
    """

    video_path = _blank_video(tmp_path)

    report = analyze_video(
        video_path,
        DeepfakeDetector(),
        FaceDetector(),
        FaceEmbeddingModel(),
    )

    frame_metadata = report["frame_metadata"]

    assert len(frame_metadata) == report["frames"]
    assert all(entry["face_detected"] is False for entry in frame_metadata)
    assert all(entry["fake_score"] == 0.0 for entry in frame_metadata)
    assert all(entry["face_x"] == 0.0 for entry in frame_metadata)

    timestamps = [entry["timestamp"] for entry in frame_metadata]
    assert timestamps == sorted(timestamps)
    assert timestamps[0] == 0.0

    # No face anywhere means the existing aggregate-score branch (the
    # "not fake_probabilities" early return) must still fire exactly as
    # before.
    assert report["fake_score"] == 0.0
    assert report["faces_detected"] == 0
