# audio-detector

Deepfake audio detection service for MithyaX. Sibling to `video-detector`,
following the same pattern: a FastAPI service the Go gateway calls into.

## Status

Phase 4, Step 3 — real AI voice-detection model integrated.
`POST /analyze-audio` decodes the uploaded file, downmixes to mono,
resamples to 16 kHz, peak-normalizes, splits into ~4s chunks, scores each
chunk with a real classifier, and returns an aggregated `fake_score` and
`verdict`. Only WAV is supported — MP3/M4A/WebM are future scope (the
decoder's entry point is already shaped for it).

**Model**: [`garystafford/wav2vec2-deepfake-voice-detector`](https://huggingface.co/garystafford/wav2vec2-deepfake-voice-detector)
(~300M params, Apache 2.0), downloaded automatically on first startup via
`transformers` and cached by Hugging Face Hub (not committed to this
repo). Loaded once at process startup, not per-request.

Note: an earlier candidate (`Mrkomiljon/voiceGUARD`) was ruled out after
checking its actual `config.json` — it has 7 unlabeled output classes,
not the 2 documented in its model card, and the model card's own example
code doesn't work against the real weights. Worth re-checking a model's
actual config before trusting its documentation.

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
  server.py       FastAPI app, routes, model lifecycle
  config.py       static configuration
  schemas.py      request/response models
  audio/
    decoder.py     WAV decoding -> waveform + metadata
    preprocess.py  mono, resample, normalize, chunk
    features.py    waveform chunk -> model input tensors
    model.py       VoiceDetector: loads the classifier, scores chunks,
                    aggregates a clip-level fake_score
```
