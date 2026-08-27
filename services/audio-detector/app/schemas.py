from __future__ import annotations

from pydantic import BaseModel


class AnalyzeAudioResponse(BaseModel):
    duration_seconds: float
    sample_rate: int
    channels: int
    chunks: int
    status: str
    fake_score: float
    verdict: str
