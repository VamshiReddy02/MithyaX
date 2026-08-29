from __future__ import annotations

import tempfile
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import urlparse

import cv2
import httpx
import numpy as np
from fastapi import FastAPI, File, HTTPException, Request, UploadFile
from pydantic import BaseModel, HttpUrl

from app.diagnostic import analyze_video, extract_face, sample_frame_metadata
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


class FaceBox(BaseModel):
    x: float
    y: float
    width: float
    height: float


class FrameMetadataResponse(BaseModel):
    timestamp: float
    fake_score: float
    face_detected: bool
    face: FaceBox | None = None


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
    # A downsampled, evenly-spaced subset of frame_metadata — see
    # sample_frame_metadata. Named separately from "frames" (the total
    # frame count above) to avoid ambiguity between the two.
    frame_metadata: list[FrameMetadataResponse]


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
        report = _run_analysis(tmp.name)

    return _build_response(video_name, report)


@app.post("/analyze-upload", response_model=AnalyzeResponse)
async def analyze_upload(video: UploadFile = File(...)) -> AnalyzeResponse:
    """Analyzes an uploaded video file directly, rather than a URL this
    service would otherwise have to fetch itself. The Go gateway's
    worker uses this path exclusively (see internal/analysisworker):
    it downloads the video through its own SSRF-safe fetcher
    (internal/security.SafeFetcher) and uploads the resulting bytes
    here, so this service never makes an outbound request to a
    client-supplied URL for this flow. /analyze (above) still exists
    for the gateway's other, older pipelines that haven't migrated to
    that model yet.
    """
    data = await video.read()
    suffix = Path(video.filename or "").suffix or ".mp4"
    video_name = video.filename or "video"

    with tempfile.NamedTemporaryFile(suffix=suffix) as tmp:
        Path(tmp.name).write_bytes(data)
        report = _run_analysis(tmp.name)

    return _build_response(video_name, report)


def _run_analysis(video_path: str) -> dict:
    try:
        return analyze_video(
            video_path,
            models.detector,
            models.face_detector,
            models.embedding_model,
        )
    except RuntimeError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc


def _build_response(video_name: str, report: dict) -> AnalyzeResponse:
    fake_score = report["fake_score"]
    sampled_frames = sample_frame_metadata(report["frame_metadata"])

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
        frame_metadata=[_frame_metadata_response(entry) for entry in sampled_frames],
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


def _frame_metadata_response(entry: dict) -> FrameMetadataResponse:
    face = None

    if entry["face_detected"]:
        face = FaceBox(
            x=entry["face_x"],
            y=entry["face_y"],
            width=entry["face_width"],
            height=entry["face_height"],
        )

    return FrameMetadataResponse(
        timestamp=entry["timestamp"],
        fake_score=entry["fake_score"],
        face_detected=entry["face_detected"],
        face=face,
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
