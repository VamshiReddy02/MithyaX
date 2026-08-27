"""The AI-generated / voice-cloning speech classifier."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import torch
from transformers import AutoFeatureExtractor, AutoModelForAudioClassification

from app.audio.features import prepare_chunk_inputs
from app.config import AUDIO_MODEL_REPO

# Label names this understands as "this chunk is fake", checked against
# the model's own config rather than assumed — models on the Hub vary in
# naming, and at least one candidate we looked at had output classes that
# didn't match its documentation at all.
_FAKE_LABEL_CANDIDATES = ("fake", "spoof", "synthetic", "ai-generated", "ai_generated")


@dataclass
class ChunkPrediction:
    fake_probability: float


class VoiceDetector:
    """Loads the configured HF audio classifier once and scores waveform chunks."""

    def __init__(self) -> None:
        self.feature_extractor = AutoFeatureExtractor.from_pretrained(AUDIO_MODEL_REPO)
        self.model = AutoModelForAudioClassification.from_pretrained(AUDIO_MODEL_REPO)
        self.model.eval()

        self.fake_label_index = self._find_fake_label_index()

    def _find_fake_label_index(self) -> int:
        label2id = {label.lower(): index for label, index in self.model.config.label2id.items()}
        for candidate in _FAKE_LABEL_CANDIDATES:
            if candidate in label2id:
                return label2id[candidate]
        raise ValueError(
            f"could not determine which label means 'fake' from label2id={self.model.config.label2id}"
        )

    def predict_chunk(self, waveform: np.ndarray) -> ChunkPrediction:
        """Score one preprocessed audio chunk (mono, float32, 16kHz, peak-normalized)."""
        inputs = prepare_chunk_inputs(self.feature_extractor, waveform)

        with torch.no_grad():
            logits = self.model(**inputs).logits

        probabilities = torch.nn.functional.softmax(logits, dim=-1)
        fake_probability = float(probabilities[0, self.fake_label_index].item())

        return ChunkPrediction(fake_probability=fake_probability)


def aggregate_fake_score(predictions: list[ChunkPrediction]) -> float:
    """Combine per-chunk fake probabilities into one score for the whole clip.

    Unlike the video detector's percentile-based aggregation (built for
    100+ frames, where a handful of outliers shouldn't dominate), an audio
    clip typically produces only a few chunks, each covering several real
    seconds of audio — so a plain mean is both simpler and more
    appropriate here.
    """
    if not predictions:
        return 0.0
    return sum(p.fake_probability for p in predictions) / len(predictions)
