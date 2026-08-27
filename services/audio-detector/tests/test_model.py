from __future__ import annotations

import numpy as np
import pytest

from app.audio.model import ChunkPrediction, VoiceDetector, aggregate_fake_score
from app.config import TARGET_SAMPLE_RATE


@pytest.fixture(scope="module")
def detector() -> VoiceDetector:
    return VoiceDetector()


def test_model_loads_and_resolves_fake_label(detector: VoiceDetector):
    assert detector.fake_label_index in (0, 1)
    assert detector.model.config.id2label[detector.fake_label_index].lower() == "fake"


def test_predict_chunk_returns_a_probability(detector: VoiceDetector):
    rng = np.random.default_rng(0)
    chunk = rng.uniform(-1.0, 1.0, size=TARGET_SAMPLE_RATE * 4).astype(np.float32)

    prediction = detector.predict_chunk(chunk)

    assert isinstance(prediction, ChunkPrediction)
    assert 0.0 <= prediction.fake_probability <= 1.0


def test_predict_chunk_handles_silence(detector: VoiceDetector):
    silence = np.zeros(TARGET_SAMPLE_RATE * 4, dtype=np.float32)

    prediction = detector.predict_chunk(silence)

    assert 0.0 <= prediction.fake_probability <= 1.0


def test_predict_chunk_handles_short_chunk(detector: VoiceDetector):
    # Shorter than a full 4s window — exercises the "short audio" case at
    # the model layer, not just chunking.
    short_chunk = np.zeros(TARGET_SAMPLE_RATE // 2, dtype=np.float32)  # 0.5s

    prediction = detector.predict_chunk(short_chunk)

    assert 0.0 <= prediction.fake_probability <= 1.0


def test_aggregate_fake_score_averages_predictions():
    predictions = [
        ChunkPrediction(fake_probability=0.2),
        ChunkPrediction(fake_probability=0.4),
        ChunkPrediction(fake_probability=0.9),
    ]

    assert aggregate_fake_score(predictions) == pytest.approx(0.5)


def test_aggregate_fake_score_empty_list_returns_zero():
    assert aggregate_fake_score([]) == 0.0
