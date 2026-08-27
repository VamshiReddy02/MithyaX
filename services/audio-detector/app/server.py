from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import FastAPI, File, HTTPException, UploadFile

from app.audio.decoder import InvalidAudioError, decode_audio
from app.audio.model import VoiceDetector, aggregate_fake_score
from app.audio.preprocess import preprocess
from app.config import APP_TITLE, FAKE_THRESHOLD
from app.schemas import AnalyzeAudioResponse


class Models:
    detector: VoiceDetector


models = Models()


@asynccontextmanager
async def lifespan(app: FastAPI):
    models.detector = VoiceDetector()
    yield


app = FastAPI(title=APP_TITLE, lifespan=lifespan)


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/analyze-audio", response_model=AnalyzeAudioResponse)
async def analyze_audio(audio: UploadFile = File(...)) -> AnalyzeAudioResponse:
    data = await audio.read()

    try:
        decoded = decode_audio(data)
    except InvalidAudioError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc

    processed = preprocess(decoded)

    predictions = [models.detector.predict_chunk(chunk) for chunk in processed.chunks]
    fake_score = aggregate_fake_score(predictions)

    return AnalyzeAudioResponse(
        duration_seconds=decoded.duration,
        sample_rate=processed.sample_rate,
        channels=1,  # preprocess() always downmixes to mono
        chunks=len(processed.chunks),
        status="processed",
        fake_score=fake_score,
        verdict="fake" if fake_score >= FAKE_THRESHOLD else "real",
    )
