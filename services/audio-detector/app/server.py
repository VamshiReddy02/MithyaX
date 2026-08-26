from __future__ import annotations

from fastapi import FastAPI, File, UploadFile

from app.config import APP_TITLE
from app.schemas import AnalyzeAudioResponse

app = FastAPI(title=APP_TITLE)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/analyze-audio", response_model=AnalyzeAudioResponse)
async def analyze_audio(audio: UploadFile = File(...)) -> AnalyzeAudioResponse:
    # Mock response — no decoding, chunking, or inference yet. That's
    # the next step, once this skeleton is proven end to end.
    return AnalyzeAudioResponse(
        audio=audio.filename or "unknown",
        duration=0,
        chunks_analyzed=0,
        fake_score=0,
        verdict="real",
    )
