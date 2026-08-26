from __future__ import annotations

import tempfile
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import urlparse

import cv2
import httpx
import numpy as np
from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, HttpUrl

from app.diagnostic import analyze_video, extract_face
from app.embedding import FaceEmbeddingModel
from app.face import FaceDetector
from app.model import DeepfakeDetector

FAKE_THRESHOLD = 0.5
DOWNLOAD_TIMEOUT_SECONDS = 60.0


class Models:
    detector: DeepfakeDetector
    face_detector: FaceDetector
    embedding_model: FaceEmbeddingModel


models = Models()


@asynccontextmanager
async def lifespan(app: FastAPI):
    models.detector = DeepfakeDetector()
    models.face_detector = FaceDetector()
    models.embedding_model = FaceEmbeddingModel()
    yield


app = FastAPI(title="MithyaX Video Detector", lifespan=lifespan)


class AnalyzeRequest(BaseModel):
    video_url: HttpUrl


class AnalyzeResponse(BaseModel):
    video: str
    frames: int
    faces_detected: int
    fake_score: float
    fake_mean: float
    fake_median: float
    fake_p75: float
    fake_p90: float
    fake_max: float
    embedding_frames: int
    verdict: str


class FrameAnalysisResponse(BaseModel):
    face_detected: bool
    fake_probability: float
    verdict: str


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/analyze", response_model=AnalyzeResponse)
def analyze(request: AnalyzeRequest) -> AnalyzeResponse:
    video_url = str(request.video_url)
    suffix = Path(urlparse(video_url).path).suffix or ".mp4"
    video_name = Path(urlparse(video_url).path).name or "video"

    with tempfile.NamedTemporaryFile(suffix=suffix) as tmp:
        _download_video(video_url, Path(tmp.name))

        try:
            report = analyze_video(
                tmp.name,
                models.detector,
                models.face_detector,
                models.embedding_model,
            )
        except RuntimeError as exc:
            raise HTTPException(status_code=422, detail=str(exc)) from exc

    fake_score = report["fake_score"]

    return AnalyzeResponse(
        video=video_name,
        frames=report["frames"],
        faces_detected=report["faces_detected"],
        fake_score=fake_score,
        fake_mean=report["fake_mean"],
        fake_median=report["fake_median"],
        fake_p75=report["fake_p75"],
        fake_p90=report["fake_p90"],
        fake_max=report["fake_max"],
        embedding_frames=report["embedding_frames"],
        verdict="fake" if fake_score >= FAKE_THRESHOLD else "real",
    )


@app.post("/analyze-frame", response_model=FrameAnalysisResponse)
async def analyze_frame(request: Request) -> FrameAnalysisResponse:
    body = await request.body()
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    frame = _decode_image(body)

    face = extract_face(frame, models.face_detector)
    if face is None:
        return FrameAnalysisResponse(face_detected=False, fake_probability=0.0, verdict="unknown")

    prediction = models.detector.predict_face(face)
    fake_probability = float(prediction["fake_probability"])

    return FrameAnalysisResponse(
        face_detected=True,
        fake_probability=fake_probability,
        verdict="fake" if fake_probability >= FAKE_THRESHOLD else "real",
    )


def _decode_image(data: bytes) -> np.ndarray:
    array = np.frombuffer(data, dtype=np.uint8)
    frame = cv2.imdecode(array, cv2.IMREAD_COLOR)

    if frame is None:
        raise HTTPException(status_code=400, detail="could not decode image")

    return frame


def _download_video(url: str, destination: Path) -> None:
    try:
        with httpx.stream(
            "GET",
            url,
            timeout=DOWNLOAD_TIMEOUT_SECONDS,
            follow_redirects=True,
        ) as response:
            response.raise_for_status()

            with destination.open("wb") as file:
                for chunk in response.iter_bytes():
                    file.write(chunk)

    except httpx.HTTPStatusError as exc:
        raise HTTPException(
            status_code=422,
            detail=f"failed to download video: HTTP {exc.response.status_code}",
        ) from exc

    except httpx.HTTPError as exc:
        raise HTTPException(
            status_code=422,
            detail=f"failed to download video: {exc}",
        ) from exc
