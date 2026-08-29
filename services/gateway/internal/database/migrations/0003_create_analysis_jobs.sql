-- The durable record of an asynchronous analysis job's lifecycle
-- (7.6.3). Redis (see internal/queue) is only the transport that
-- delivers a job to a worker; this table is the source of truth for
-- where a job actually stands (queued/processing/completed/failed/
-- dead_letter), independent of its current position in a Redis list —
-- so a client can poll GET /api/v1/analysis/jobs/:id without any of
-- this depending on Redis being reachable, and so a job's history
-- survives a Redis flush.
CREATE TABLE IF NOT EXISTS analysis_jobs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    type TEXT NOT NULL,
    status TEXT NOT NULL,
    attempt INT NOT NULL DEFAULT 1,
    max_attempts INT NOT NULL,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

-- Supports the completion coordinator's "is there an outstanding job
-- of the other modality for this session" lookup (7.6.6).
CREATE INDEX IF NOT EXISTS analysis_jobs_session_id_type_idx ON analysis_jobs (session_id, type);
