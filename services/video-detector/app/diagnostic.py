from __future__ import annotations

from dataclasses import asdict, dataclass
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

# Fallback used when a video's container doesn't report a usable FPS
# (some webm/mkv sources report 0). Only affects the per-frame
# timestamps in FrameMetadata — it doesn't touch the aggregate score.
DEFAULT_FPS = 30.0

# How many frame-metadata entries analyze() returns over the API. The
# analysis loop itself still processes every frame — this only bounds
# what's sent back through the gateway, since a 30-second clip can
# easily produce hundreds or thousands of frames and the temporal
# analyzer only needs a representative, evenly-spaced sample of them.
MAX_FRAME_METADATA = 60


@dataclass
class FrameMetadata:
    """
    One analyzed frame's metadata — the per-frame signal the Go
    temporal analyzer (internal/temporal.Frame) needs, one field for
    one field.

    face_x/y/width/height are 0.0 when face_detected is False; there's
    no box to report.
    """

    timestamp: float
    fake_score: float
    face_detected: bool
    face_x: float = 0.0
    face_y: float = 0.0
    face_width: float = 0.0
    face_height: float = 0.0


def _detect_face_box(
    frame: np.ndarray,
    face_detector: FaceDetector,
):
    """
    Detect the largest face in a frame and return its bounding box,
    clipped to the frame.

    Returns (x, y, width, height), or None if no face was found.
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

    return (x1, y1, x2 - x1, y2 - y1)


def extract_face(
    frame: np.ndarray,
    face_detector: FaceDetector,
):
    """
    Detect the largest face in a frame.

    Returns the face crop.
    """

    box = _detect_face_box(frame, face_detector)

    if box is None:
        return None

    x, y, w, h = box

    return frame[
        y:y + h,
        x:x + w,
    ]


def sample_frame_metadata(
    entries: list[dict],
    max_samples: int = MAX_FRAME_METADATA,
):
    """
    Evenly downsample frame metadata down to at most max_samples
    entries, always keeping the first and last frame.

    This is purely a transport-size decision: it exists so /analyze
    doesn't ship hundreds or thousands of entries through the gateway
    for a long clip. It has no effect on the aggregate video score,
    which is computed from every frame before this ever runs.
    """

    total = len(entries)

    if total <= max_samples or max_samples <= 1:
        return list(entries)

    indices = [
        round(i * (total - 1) / (max_samples - 1))
        for i in range(max_samples)
    ]

    # Rounding can repeat an index when max_samples is close to total;
    # de-duplicate while preserving chronological order.
    seen = set()
    sampled = []

    for index in indices:
        if index in seen:
            continue

        seen.add(index)
        sampled.append(entries[index])

    return sampled


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

    fps = cap.get(cv2.CAP_PROP_FPS)

    if not fps or fps <= 0:
        fps = DEFAULT_FPS

    frame_count = 0
    faces_detected = 0

    fake_probabilities = []
    embeddings = []
    frame_metadata: list[FrameMetadata] = []

    while True:
        ok, frame = cap.read()

        if not ok:
            break

        frame_count += 1
        timestamp = (frame_count - 1) / fps

        box = _detect_face_box(
            frame,
            face_detector,
        )

        if box is None:
            frame_metadata.append(
                FrameMetadata(
                    timestamp=timestamp,
                    fake_score=0.0,
                    face_detected=False,
                )
            )
            continue

        face_x, face_y, face_width, face_height = box
        face = frame[
            face_y:face_y + face_height,
            face_x:face_x + face_width,
        ]

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

        frame_metadata.append(
            FrameMetadata(
                timestamp=timestamp,
                fake_score=fake_probability,
                face_detected=True,
                face_x=float(face_x),
                face_y=float(face_y),
                face_width=float(face_width),
                face_height=float(face_height),
            )
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

    frame_metadata_dicts = [
        asdict(entry) for entry in frame_metadata
    ]

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
            "frame_metadata": frame_metadata_dicts,
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

        "frame_metadata": frame_metadata_dicts,
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