from __future__ import annotations

import numpy as np


def cosine_similarity(
    a: np.ndarray,
    b: np.ndarray,
) -> float:
    """
    Cosine similarity between two embeddings.
    """

    a = np.asarray(a, dtype=np.float32)
    b = np.asarray(b, dtype=np.float32)

    denominator = (
        np.linalg.norm(a)
        * np.linalg.norm(b)
    )

    if denominator == 0:
        return 0.0

    return float(
        np.dot(a, b) / denominator
    )


def weighted_temporal_similarity(
    previous_embedding: np.ndarray,
    current_embedding: np.ndarray,
    previous_quality: float,
    current_quality: float,
) -> dict:
    """
    Calculate temporal similarity while taking
    face quality into account.
    """

    similarity = cosine_similarity(
        previous_embedding,
        current_embedding,
    )

    quality = min(
        previous_quality,
        current_quality,
    )

    return {
        "similarity": similarity,
        "quality": quality,
        "usable": quality >= 0.45,
    }


def calculate_embedding_consistency(
    embeddings: list[np.ndarray],
) -> dict | None:
    """
    Measure consistency between consecutive
    face embeddings.

    Higher similarity means the face representation
    is more consistent across frames.

    This is diagnostic information only.
    """

    if embeddings is None:
        return None

    if len(embeddings) < 2:
        return None

    similarities = []

    for previous, current in zip(
        embeddings[:-1],
        embeddings[1:],
    ):
        similarity = cosine_similarity(
            previous,
            current,
        )

        similarities.append(
            similarity
        )

    if not similarities:
        return None

    similarities = np.asarray(
        similarities,
        dtype=np.float32,
    )

    return {
        "mean_similarity": float(
            np.mean(similarities)
        ),
        "median_similarity": float(
            np.median(similarities)
        ),
        "p10_similarity": float(
            np.percentile(
                similarities,
                10,
            )
        ),
        "min_similarity": float(
            np.min(similarities)
        ),
    }


def calculate_temporal_changes(
    embeddings: list[np.ndarray],
) -> dict | None:
    """
    Measure changes between consecutive face embeddings.

    This is diagnostic information only.

    Large changes can happen naturally because of:
    - dancing
    - head movement
    - pose changes
    - motion blur
    - face detection changes

    They should NOT independently determine
    whether a video is fake.
    """

    if embeddings is None:
        return None

    if len(embeddings) < 2:
        return None

    similarities = []

    for previous, current in zip(
        embeddings[:-1],
        embeddings[1:],
    ):
        similarity = cosine_similarity(
            previous,
            current,
        )

        similarities.append(
            similarity
        )

    if not similarities:
        return None

    similarities = np.asarray(
        similarities,
        dtype=np.float32,
    )

    changes = 1.0 - similarities

    # A change above 0.15 is considered
    # a relatively large frame-to-frame change.
    #
    # This is only a diagnostic threshold.
    large_changes = changes > 0.15

    return {
        "mean_change": float(
            np.mean(changes)
        ),

        "median_change": float(
            np.median(changes)
        ),

        "p90_change": float(
            np.percentile(
                changes,
                90,
            )
        ),

        "max_change": float(
            np.max(changes)
        ),

        "large_change_ratio": float(
            np.mean(large_changes)
        ),
    }