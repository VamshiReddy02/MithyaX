"""Static configuration for the audio-detector service."""

APP_TITLE = "MithyaX Audio Detector"

# Every waveform is downmixed and resampled to this rate before chunking
# — the rate the voice detector expects to see.
TARGET_SAMPLE_RATE = 16_000

# Chunk window, in seconds. 4s sits in the requested 3-5s range.
CHUNK_SECONDS = 4.0

# AI-generated / voice-cloning speech classifier. Verified against its
# actual config (not just its model card) before picking it: id2label is
# {0: "real", 1: "fake"}, no ambiguity about what the output means.
AUDIO_MODEL_REPO = "garystafford/wav2vec2-deepfake-voice-detector"

# Threshold above which a fake_score is reported as "fake" rather than
# "real".
FAKE_THRESHOLD = 0.5
