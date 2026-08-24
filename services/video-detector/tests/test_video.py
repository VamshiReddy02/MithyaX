import cv2
import numpy as np
import pytest

from PIL import Image

from app.model import DeepfakeDetector
from app.video import sample_frames


def test_model_loads_and_predicts():
    detector = DeepfakeDetector()

    image = Image.new(
        "RGB",
        (224, 224),
        color="gray",
    )

    result = detector.predict_image(image)

    assert "real_probability" in result
    assert "fake_probability" in result
    assert "label" in result

    assert 0.0 <= result["real_probability"] <= 1.0
    assert 0.0 <= result["fake_probability"] <= 1.0

    assert result["label"] in {
        "REAL",
        "FAKE",
    }


def test_sample_frames(tmp_path):
    """
    Create a temporary video instead of depending on
    samples/test.mp4 existing in the repository.
    """

    video_path = tmp_path / "test.mp4"

    fourcc = cv2.VideoWriter_fourcc(*"mp4v")

    writer = cv2.VideoWriter(
        str(video_path),
        fourcc,
        10.0,
        (640, 480),
    )

    if not writer.isOpened():
        pytest.skip(
            "OpenCV cannot create MP4 videos in this environment"
        )

    try:
        for _ in range(16):
            frame = np.zeros(
                (480, 640, 3),
                dtype=np.uint8,
            )

            writer.write(frame)

    finally:
        writer.release()

    frames = sample_frames(str(video_path))

    assert frames is not None
    assert len(frames) > 0

    for frame in frames:
        assert isinstance(frame, np.ndarray)
        assert frame.ndim == 3
        assert frame.shape[2] == 3