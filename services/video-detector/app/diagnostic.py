from __future__ import annotations

from pathlib import Path

import cv2
import numpy as np

from app.embedding import FaceEmbeddingModel
from app.face import FaceDetector
from app.model import DeepfakeDetector


VIDEO_EXTENSIONS = {
    ".mp4",
    ".mov",
    ".avi",
    ".mkv",
    ".webm",
}


def extract_face(
    frame: np.ndarray,
    face_detector: FaceDetector,
):
    """
    Detect the largest face in a frame.

    Returns the face crop.
    """

    faces = face_detector.detect(frame)

    if faces is None or len(faces) == 0:
        return None

    largest = max(
        faces,
        key=lambda face: float(
            face[2] * face[3]
        ),
    )

    x, y, w, h = [
        int(value)
        for value in largest[:4]
    ]

    height, width = frame.shape[:2]

    x1 = max(0, x)
    y1 = max(0, y)

    x2 = min(
        width,
        x + max(w, 1),
    )

    y2 = min(
        height,
        y + max(h, 1),
    )

    if x2 <= x1 or y2 <= y1:
        return None

    return frame[
        y1:y2,
        x1:x2,
    ]


def robust_fake_score(
    probabilities: np.ndarray,
):
    """
    Calculate a robust video-level fake score.

    We deliberately avoid allowing a few extreme
    frames to dominate the entire video.

    This is especially important for dancing videos,
    where motion, blur and pose changes can create
    occasional high-confidence predictions.
    """

    if len(probabilities) == 0:
        return 0.0

    median = float(
        np.median(probabilities)
    )

    p75 = float(
        np.percentile(
            probabilities,
            75,
        )
    )

    p90 = float(
        np.percentile(
            probabilities,
            90,
        )
    )

    mean = float(
        np.mean(probabilities)
    )

    # Median gets the largest weight.
    #
    # This means isolated 0.99/1.0 predictions
    # don't automatically make a video fake.

    score = (
        0.50 * median
        + 0.25 * p75
        + 0.15 * p90
        + 0.10 * mean
    )

    return float(
        np.clip(score, 0.0, 1.0)
    )


def analyze_video(
    video_path: str | Path,
    detector: DeepfakeDetector,
    face_detector: FaceDetector,
    embedding_model: FaceEmbeddingModel,
):
    """
    Analyze a video.

    Xception is the primary signal.

    Face embeddings and temporal changes are
    diagnostic signals only.
    """

    video_path = Path(video_path)

    cap = cv2.VideoCapture(
        str(video_path)
    )

    if not cap.isOpened():
        raise RuntimeError(
            f"Could not open video: {video_path}"
        )

    frame_count = 0
    faces_detected = 0

    fake_probabilities = []
    embeddings = []

    while True:
        ok, frame = cap.read()

        if not ok:
            break

        frame_count += 1

        face = extract_face(
            frame,
            face_detector,
        )

        if face is None:
            continue

        faces_detected += 1

        # --------------------------------
        # XCEPTION
        # --------------------------------

        prediction = detector.predict_face(
            face
        )

        fake_probability = float(
            prediction["fake_probability"]
        )

        fake_probabilities.append(
            fake_probability
        )

        # --------------------------------
        # EMBEDDING
        # --------------------------------

        try:
            embedding = embedding_model.embed(
                face
            )

            embeddings.append(
                embedding
            )

        except Exception as exc:
            print(
                f"Embedding failed on frame "
                f"{frame_count}: {exc}"
            )

    cap.release()

    if not fake_probabilities:
        return {
            "video": video_path.name,
            "frames": frame_count,
            "faces_detected": 0,
            "fake_mean": 0.0,
            "fake_median": 0.0,
            "fake_p75": 0.0,
            "fake_p90": 0.0,
            "fake_max": 0.0,
            "fake_score": 0.0,
            "embedding_frames": 0,
            "embedding_consistency": None,
            "temporal_changes": None,
        }

    probabilities = np.asarray(
        fake_probabilities,
        dtype=np.float32,
    )

    # --------------------------------
    # ROBUST XCEPTION STATISTICS
    # --------------------------------

    fake_mean = float(
        np.mean(probabilities)
    )

    fake_median = float(
        np.median(probabilities)
    )

    fake_p75 = float(
        np.percentile(
            probabilities,
            75,
        )
    )

    fake_p90 = float(
        np.percentile(
            probabilities,
            90,
        )
    )

    fake_max = float(
        np.max(probabilities)
    )

    fake_score = robust_fake_score(
        probabilities
    )

    # --------------------------------
    # EMBEDDING DIAGNOSTICS
    # --------------------------------

    from app.temporal import (
        calculate_embedding_consistency,
        calculate_temporal_changes,
    )

    consistency = (
        calculate_embedding_consistency(
            embeddings
        )
        if len(embeddings) >= 2
        else None
    )

    temporal = (
        calculate_temporal_changes(
            embeddings
        )
        if len(embeddings) >= 2
        else None
    )

    return {
        "video": video_path.name,

        "frames": frame_count,

        "faces_detected": faces_detected,

        "fake_mean": fake_mean,

        "fake_median": fake_median,

        "fake_p75": fake_p75,

        "fake_p90": fake_p90,

        "fake_max": fake_max,

        "fake_score": fake_score,

        "embedding_frames": len(
            embeddings
        ),

        "embedding_consistency": consistency,

        "temporal_changes": temporal,
    }


def collect_videos(
    dataset_dir: str | Path,
):
    dataset_dir = Path(dataset_dir)

    return sorted(
        path
        for path in dataset_dir.iterdir()
        if (
            path.is_file()
            and path.suffix.lower()
            in VIDEO_EXTENSIONS
        )
    )