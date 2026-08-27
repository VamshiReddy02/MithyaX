"""Turns preprocessed waveform chunks into model-ready input tensors."""

from __future__ import annotations

from typing import TYPE_CHECKING

import numpy as np

from app.config import TARGET_SAMPLE_RATE

if TYPE_CHECKING:
    from transformers import AutoFeatureExtractor


def prepare_chunk_inputs(feature_extractor: "AutoFeatureExtractor", chunk: np.ndarray):
    """Convert one preprocessed audio chunk (mono, float32, TARGET_SAMPLE_RATE,
    peak-normalized) into the model's expected input tensors.
    """
    return feature_extractor(
        chunk,
        sampling_rate=TARGET_SAMPLE_RATE,
        return_tensors="pt",
        padding=True,
    )
