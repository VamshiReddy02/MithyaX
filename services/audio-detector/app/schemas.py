from __future__ import annotations

from pydantic import BaseModel


class AnalyzeAudioResponse(BaseModel):
    audio: str
    duration: float
    chunks_analyzed: int
    fake_score: float
    verdict: str
