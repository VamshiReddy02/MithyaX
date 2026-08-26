"""Static configuration for the audio-detector service."""

APP_TITLE = "MithyaX Audio Detector"

# Threshold above which a fake_score is reported as "fake" rather than
# "real". Unused until Phase 4 Step 2 wires in real inference.
FAKE_THRESHOLD = 0.5
