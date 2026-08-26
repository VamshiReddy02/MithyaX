# audio-detector

Deepfake audio detection service for MithyaX. Sibling to `video-detector`,
following the same pattern: a FastAPI service the Go gateway calls into.

## Status

Phase 4, Step 1 — skeleton only. `POST /analyze-audio` returns a mock
response; no audio decoding or ML inference is wired up yet.

## Run locally

```bash
cd services/audio-detector
source .venv/bin/activate
uvicorn app.server:app --host 127.0.0.1 --port 8001
```

```bash
curl http://127.0.0.1:8001/health

curl -X POST http://127.0.0.1:8001/analyze-audio \
  -F "audio=@samples/test.wav"
```

## Test

```bash
pytest -v
```

## Layout

```
app/
  server.py       FastAPI app and routes
  config.py       static configuration
  schemas.py      request/response models
  audio/
    loader.py         audio decoding (not implemented yet)
    preprocessing.py  normalization/chunking (not implemented yet)
```
