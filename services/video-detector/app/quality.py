from __future__ import annotations

import cv2
import numpy as np


def calculate_blur_score(face: np.ndarray) -> float:
    """
    Estimate face sharpness using variance of Laplacian.

    Higher = sharper
    Lower = blurrier
    """
    if face.size == 0:
        return 0.0

    gray = cv2.cvtColor(face, cv2.COLOR_BGR2GRAY)
    return float(cv2.Laplacian(gray, cv2.CV_64F).var())


def calculate_brightness(face: np.ndarray) -> float:
    """
    Average grayscale brightness, 0-255.
    """
    if face.size == 0:
        return 0.0

    gray = cv2.cvtColor(face, cv2.COLOR_BGR2GRAY)
    return float(np.mean(gray))


def calculate_face_area(
    bbox: tuple[int, int, int, int],
    frame_width: int,
    frame_height: int,
) -> float:
    """
    Fraction of the frame occupied by the face.
    """
    x1, y1, x2, y2 = bbox

    width = max(0, x2 - x1)
    height = max(0, y2 - y1)

    face_area = width * height
    frame_area = frame_width * frame_height

    if frame_area == 0:
        return 0.0

    return float(face_area / frame_area)


def calculate_face_quality(
    face: np.ndarray,
    bbox: tuple[int, int, int, int],
    frame_width: int,
    frame_height: int,
) -> dict:
    """
    Calculate quality metrics for a detected face.
    """

    if face.size == 0:
        return {
            "blur_score": 0.0,
            "brightness": 0.0,
            "face_area_ratio": 0.0,
            "quality": 0.0,
        }

    blur_score = calculate_blur_score(face)
    brightness = calculate_brightness(face)
    face_area_ratio = calculate_face_area(
        bbox,
        frame_width,
        frame_height,
    )

    # ---------------------------------------------------------
    # Normalize blur
    # ---------------------------------------------------------
    #
    # This is intentionally conservative.
    # We don't want blur alone to classify a video.
    #
    blur_quality = np.clip(blur_score / 150.0, 0.0, 1.0)

    # ---------------------------------------------------------
    # Brightness quality
    # ---------------------------------------------------------

    # Ideal region roughly around 70-190.
    if brightness < 40:
        brightness_quality = brightness / 40.0
    elif brightness > 220:
        brightness_quality = (255.0 - brightness) / 35.0
    else:
        brightness_quality = 1.0

    brightness_quality = float(
        np.clip(brightness_quality, 0.0, 1.0)
    )

    # ---------------------------------------------------------
    # Face-size quality
    # ---------------------------------------------------------

    # Very small faces are unreliable.
    #
    # 0.01 = 1% of frame
    # 0.05 = 5% of frame
    #

    if face_area_ratio < 0.005:
        size_quality = 0.0
    elif face_area_ratio < 0.02:
        size_quality = face_area_ratio / 0.02
    else:
        size_quality = 1.0

    size_quality = float(
        np.clip(size_quality, 0.0, 1.0)
    )

    # ---------------------------------------------------------
    # Combined quality
    # ---------------------------------------------------------

    quality = (
        0.50 * blur_quality
        + 0.25 * brightness_quality
        + 0.25 * size_quality
    )

    return {
        "blur_score": float(blur_score),
        "brightness": float(brightness),
        "face_area_ratio": float(face_area_ratio),
        "quality": float(np.clip(quality, 0.0, 1.0)),
    }