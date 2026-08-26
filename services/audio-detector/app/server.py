from __future__ import annotations

from fastapi import FastAPI, File, HTTPException, UploadFile

from app.audio.loader import InvalidAudioError, load_wav
from app.config import APP_TITLE
from app.schemas import AnalyzeAudioResponse

app = FastAPI(title=APP_TITLE)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/analyze-audio", response_model=AnalyzeAudioResponse)
async def analyze_audio(audio: UploadFile = File(...)) -> AnalyzeAudioResponse:
    data = await audio.read()

    try:
        decoded = load_wav(data)
    except InvalidAudioError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc

    return AnalyzeAudioResponse(
        audio=audio.filename or "unknown",
        duration=decoded.duration,
        sample_rate=decoded.sample_rate,
        channels=decoded.channels,
    )
