from __future__ import annotations

from pydantic import BaseModel


class AnalyzeAudioResponse(BaseModel):
    audio: str
    duration: float
    sample_rate: int
    channels: int
