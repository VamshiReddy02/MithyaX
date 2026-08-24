from __future__ import annotations

from pathlib import Path
import sys

import cv2
import numpy as np

from app.face import FaceDetector
from app.model import DeepfakeDetector


VIDEO_EXTENSIONS = {
    ".mp4",
    ".mov",
    ".avi",
    ".mkv",
    ".webm",
}

# A frame is considered suspicious above this probability.
SUSPICIOUS_THRESHOLD = 0.50

# Video-level classification thresholds.
FAKE_THRESHOLD = 0.55
REAL_THRESHOLD = 0.35


def collect_videos(directory: str | Path) -> list[Path]:
    """
    Collect supported video files from a directory.
    """

    directory = Path(directory)

    if not directory.exists():
        return []

    if not directory.is_dir():
        return []

    return sorted(
        path
        for path in directory.iterdir()
        if path.is_file()
        and path.suffix.lower() in VIDEO_EXTENSIONS
    )


def extract_faces(
    video_path: str | Path,
    face_detector: FaceDetector,
) -> tuple[int, int, list[np.ndarray]]:
    """
    Read a video frame-by-frame and extract the largest
    detected face from each frame.

    Returns:

        total_frames
        faces_detected
        faces
    """

    video_path = Path(video_path)

    cap = cv2.VideoCapture(str(video_path))

    if not cap.isOpened():
        raise RuntimeError(
            f"Could not open video: {video_path}"
        )

    total_frames = 0
    faces_detected = 0

    faces: list[np.ndarray] = []

    while True:
        ok, frame = cap.read()

        if not ok:
            break

        total_frames += 1

        detected = face_detector.detect(frame)

        if detected is None or len(detected) == 0:
            continue

        # Select the largest face.
        largest = max(
            detected,
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
            continue

        face = frame[
            y1:y2,
            x1:x2,
        ]

        if face.size == 0:
            continue

        faces_detected += 1

        faces.append(face)

    cap.release()

    return (
        total_frames,
        faces_detected,
        faces,
    )


def calculate_video_score(
    fake_probabilities: np.ndarray,
    suspicious_ratio: float,
) -> float:
    """
    Convert frame-level fake probabilities into
    a single video-level fake score.

    We intentionally use several statistics rather
    than relying only on the mean.
    """

    if len(fake_probabilities) == 0:
        return 0.0

    mean_score = float(
        np.mean(fake_probabilities)
    )

    median_score = float(
        np.median(fake_probabilities)
    )

    p75_score = float(
        np.percentile(
            fake_probabilities,
            75,
        )
    )

    p90_score = float(
        np.percentile(
            fake_probabilities,
            90,
        )
    )

    max_score = float(
        np.max(fake_probabilities)
    )

    # Weighted combination.
    #
    # Mean:
    #   Overall behavior of the video.
    #
    # Median:
    #   Robust against a few extreme frames.
    #
    # P75/P90:
    #   Captures videos where a significant portion
    #   of frames look manipulated.
    #
    # Max:
    #   Weak signal; only contributes a little.
    #
    # Suspicious ratio:
    #   How much of the video crosses the suspicious
    #   frame threshold.

    score = (
        0.25 * mean_score
        + 0.15 * median_score
        + 0.20 * p75_score
        + 0.25 * p90_score
        + 0.05 * max_score
        + 0.10 * suspicious_ratio
    )

    return float(
        np.clip(score, 0.0, 1.0)
    )


def classify_score(score: float) -> str:
    """
    Convert a video-level fake score into:

        REAL
        UNCERTAIN
        FAKE
    """

    if score >= FAKE_THRESHOLD:
        return "FAKE"

    if score <= REAL_THRESHOLD:
        return "REAL"

    return "UNCERTAIN"


def evaluate_video(
    detector: DeepfakeDetector,
    face_detector: FaceDetector,
    video_path: str | Path,
    expected: str,
) -> dict:
    """
    Evaluate one video using:

        Video
          ↓
        Face detection
          ↓
        Face crops
          ↓
        Xception
          ↓
        Frame probabilities
          ↓
        Video-level statistics
          ↓
        Video score
          ↓
        REAL / UNCERTAIN / FAKE
    """

    (
        total_frames,
        faces_detected,
        faces,
    ) = extract_faces(
        video_path,
        face_detector,
    )

    if total_frames == 0:
        raise RuntimeError(
            f"No frames extracted: {video_path}"
        )

    if not faces:
        raise RuntimeError(
            f"No faces detected: {video_path}"
        )

    # Predict every detected face.
    predictions = []

    for face in faces:
        try:
            prediction = detector.predict_face(
                face
            )

            predictions.append(
                prediction
            )

        except Exception as exc:
            print(
                f"       WARNING: face prediction failed: "
                f"{exc}"
            )

    if not predictions:
        raise RuntimeError(
            f"No face predictions generated: "
            f"{video_path}"
        )

    fake_probabilities = np.asarray(
        [
            float(
                prediction["fake_probability"]
            )
            for prediction in predictions
        ],
        dtype=np.float32,
    )

    # --------------------------------------------------
    # Frame statistics
    # --------------------------------------------------

    mean_fake = float(
        np.mean(fake_probabilities)
    )

    median_fake = float(
        np.median(fake_probabilities)
    )

    p75_fake = float(
        np.percentile(
            fake_probabilities,
            75,
        )
    )

    p90_fake = float(
        np.percentile(
            fake_probabilities,
            90,
        )
    )

    max_fake = float(
        np.max(fake_probabilities)
    )

    # --------------------------------------------------
    # Suspicious frames
    # --------------------------------------------------

    suspicious_mask = (
        fake_probabilities
        >= SUSPICIOUS_THRESHOLD
    )

    suspicious_frames = int(
        np.sum(suspicious_mask)
    )

    frames_analyzed = len(
        fake_probabilities
    )

    suspicious_ratio = (
        suspicious_frames / frames_analyzed
        if frames_analyzed
        else 0.0
    )

    # --------------------------------------------------
    # Video score
    # --------------------------------------------------

    video_score = calculate_video_score(
        fake_probabilities,
        suspicious_ratio,
    )

    predicted = classify_score(
        video_score
    )

    return {
        "video": Path(video_path).name,
        "expected": expected,
        "predicted": predicted,
        "frames": total_frames,
        "faces_detected": faces_detected,
        "frames_analyzed": frames_analyzed,
        "mean_fake_probability": mean_fake,
        "median_fake_probability": median_fake,
        "p75_fake_probability": p75_fake,
        "p90_fake_probability": p90_fake,
        "max_fake_probability": max_fake,
        "suspicious_frames": suspicious_frames,
        "suspicious_frame_ratio": suspicious_ratio,
        "video_score": video_score,
    }


def calculate_metrics(
    results: list[dict],
) -> dict:
    """
    Calculate binary classification metrics.

    UNCERTAIN is treated as incorrect.
    """

    tp = 0
    tn = 0
    fp = 0
    fn = 0

    for result in results:
        actual = result["expected"]
        predicted = result["predicted"]

        if actual == "FAKE":
            if predicted == "FAKE":
                tp += 1
            else:
                fn += 1

        elif actual == "REAL":
            if predicted == "REAL":
                tn += 1
            else:
                fp += 1

    total = (
        tp
        + tn
        + fp
        + fn
    )

    accuracy = (
        (tp + tn) / total
        if total
        else 0.0
    )

    precision = (
        tp / (tp + fp)
        if tp + fp
        else 0.0
    )

    recall = (
        tp / (tp + fn)
        if tp + fn
        else 0.0
    )

    f1 = (
        2 * precision * recall
        / (precision + recall)
        if precision + recall
        else 0.0
    )

    return {
        "accuracy": accuracy,
        "precision": precision,
        "recall": recall,
        "f1": f1,
        "tp": tp,
        "tn": tn,
        "fp": fp,
        "fn": fn,
    }


def print_result(result: dict) -> None:
    """
    Print the result for one video.
    """

    print(
        f"       Frames: "
        f"{result['frames']}"
    )

    print(
        f"       Faces detected: "
        f"{result['faces_detected']}"
    )

    print(
        f"       Frames analyzed: "
        f"{result['frames_analyzed']}"
    )

    print(
        f"       Mean:   "
        f"{result['mean_fake_probability']:.4f}"
    )

    print(
        f"       Median: "
        f"{result['median_fake_probability']:.4f}"
    )

    print(
        f"       P75:    "
        f"{result['p75_fake_probability']:.4f}"
    )

    print(
        f"       P90:    "
        f"{result['p90_fake_probability']:.4f}"
    )

    print(
        f"       Max:    "
        f"{result['max_fake_probability']:.4f}"
    )

    print(
        f"       Suspicious frames: "
        f"{result['suspicious_frames']}/"
        f"{result['frames_analyzed']} "
        f"("
        f"{result['suspicious_frame_ratio']:.2%}"
        f")"
    )

    print(
        f"       Video score: "
        f"{result['video_score']:.4f}"
    )

    print(
        f"       -> "
        f"{result['predicted']}"
    )


def print_summary(
    results: list[dict],
    metrics: dict,
) -> None:
    """
    Print final evaluation summary.
    """

    print()
    print("Results")
    print("-------")
    print()

    print(
        f"Videos evaluated: "
        f"{len(results)}"
    )

    print(
        f"Accuracy:         "
        f"{metrics['accuracy']:.4f}"
    )

    print(
        f"Precision:        "
        f"{metrics['precision']:.4f}"
    )

    print(
        f"Recall:           "
        f"{metrics['recall']:.4f}"
    )

    print(
        f"F1 Score:         "
        f"{metrics['f1']:.4f}"
    )

    print()
    print("Confusion Matrix")
    print("----------------")
    print()

    print(
        "                 Predicted"
    )

    print(
        "                 REAL   FAKE"
    )

    print(
        f"Actual REAL     "
        f"{metrics['tn']:4d}   "
        f"{metrics['fp']:4d}"
    )

    print(
        f"Actual FAKE     "
        f"{metrics['fn']:4d}   "
        f"{metrics['tp']:4d}"
    )

    # --------------------------------------------------
    # Incorrect predictions
    # --------------------------------------------------

    incorrect = [
        result
        for result in results
        if (
            result["expected"]
            != result["predicted"]
        )
    ]

    print()
    print("Incorrect Predictions")
    print("---------------------")

    if not incorrect:
        print("None")
        return

    for result in incorrect:
        print(
            f"{result['video']}: "
            f"expected="
            f"{result['expected']} "
            f"predicted="
            f"{result['predicted']} "
            f"score="
            f"{result['video_score']:.4f}"
        )


def main() -> None:
    """
    Entry point.

    Usage:

        python -m app.evaluate samples/dataset
    """

    if len(sys.argv) != 2:
        print(
            "Usage: "
            "python -m app.evaluate <dataset>"
        )
        sys.exit(1)

    dataset_dir = Path(
        sys.argv[1]
    )

    real_dir = dataset_dir / "real"
    fake_dir = dataset_dir / "fake"

    real_videos = collect_videos(
        real_dir
    )

    fake_videos = collect_videos(
        fake_dir
    )

    print()
    print("MithyaX Evaluation")
    print("==================")
    print()

    print(
        f"Real videos: "
        f"{len(real_videos)}"
    )

    print(
        f"Fake videos: "
        f"{len(fake_videos)}"
    )

    # --------------------------------------------------
    # Dataset warnings
    # --------------------------------------------------

    if len(real_videos) == 0:
        print()
        print(
            "WARNING: No REAL videos found."
        )

    if len(fake_videos) == 0:
        print()
        print(
            "WARNING: No FAKE videos found."
        )

    if (
        real_videos
        and fake_videos
        and len(real_videos)
        != len(fake_videos)
    ):
        print()
        print(
            "WARNING: Dataset is imbalanced."
        )

        print(
            "For meaningful accuracy evaluation, "
            "try to use similar numbers of REAL "
            "and FAKE videos."
        )

    if (
        len(real_videos) == 0
        or len(fake_videos) == 0
    ):
        print()
        print(
            "WARNING: Both REAL and FAKE "
            "videos are required for evaluation."
        )

    # --------------------------------------------------
    # Load models
    # --------------------------------------------------

    print()
    print(
        "Loading Xception detector..."
    )

    detector = DeepfakeDetector()

    print(
        "Loading face detector..."
    )

    face_detector = FaceDetector()

    print()
    print("Evaluating...")
    print()

    # --------------------------------------------------
    # Build evaluation list
    # --------------------------------------------------

    all_videos = [
        ("REAL", video)
        for video in real_videos
    ]

    all_videos.extend(
        [
            ("FAKE", video)
            for video in fake_videos
        ]
    )

    results: list[dict] = []

    # --------------------------------------------------
    # Evaluate videos
    # --------------------------------------------------

    for expected, video_path in all_videos:

        print(
            f"[{expected}] "
            f"{video_path.name}"
        )

        try:
            result = evaluate_video(
                detector=detector,
                face_detector=face_detector,
                video_path=video_path,
                expected=expected,
            )

            results.append(result)

            print_result(result)

        except Exception as exc:
            print(
                f"       ERROR: {exc}"
            )

        print()

    # --------------------------------------------------
    # Final results
    # --------------------------------------------------

    if not results:
        print(
            "No videos were successfully "
            "evaluated."
        )
        return

    metrics = calculate_metrics(
        results
    )

    print_summary(
        results,
        metrics,
    )


if __name__ == "__main__":
    main()