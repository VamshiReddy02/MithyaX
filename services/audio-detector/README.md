# audio-detector

Deepfake audio detection service for MithyaX. Sibling to `video-detector`,
following the same pattern: a FastAPI service the Go gateway calls into.

## Status

Phase 4, Step 2 — WAV loading and decoding. `POST /analyze-audio` decodes
the uploaded WAV file and returns its real metadata (duration, sample
rate, channel count). No ML inference is wired up yet, and only WAV is
supported — MP3/M4A/WebM are future scope.

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
    loader.py         WAV decoding -> waveform + metadata
    preprocessing.py  normalization/chunking (not implemented yet)
```
