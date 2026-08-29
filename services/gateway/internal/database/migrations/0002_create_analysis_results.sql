CREATE TABLE IF NOT EXISTS analysis_results (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id),
    video_fake_score DOUBLE PRECISION,
    video_verdict TEXT,
    audio_fake_score DOUBLE PRECISION,
    audio_verdict TEXT,
    temporal_score DOUBLE PRECISION,
    risk_score DOUBLE PRECISION NOT NULL,
    risk_verdict TEXT NOT NULL,
    risk_reasons TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
