// Package analysis persists the per-modality breakdown behind a
// completed session's risk assessment — video/audio/temporal scores,
// each modality's own verdict, and the reasons the risk engine gave —
// so "why did MithyaX classify this meeting as HIGH RISK?" can be
// answered after the fact, not just "what was the final score."
//
// This is deliberately a separate table (and package) from
// internal/repository/sessions rather than more columns bolted onto
// sessions: sessions answers "what happened to session X" at a glance,
// this answers "why," and the two are written together but read for
// different reasons.
package analysis

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by GetBySessionID when no analysis result
// exists for the given session ID.
var ErrNotFound = errors.New("analysis result not found")

// Result is the persisted analysis record for one completed session.
// Per-modality fields are pointers/empty-string because a session can
// complete having only ever gathered some of its signals (e.g. video
// never detected a face) — the same "missing means absent, not a
// confident zero" rule risk.Signals already follows.
type Result struct {
	SessionID string `json:"session_id"`

	VideoFakeScore *float64 `json:"video_fake_score,omitempty"`
	VideoVerdict   string   `json:"video_verdict,omitempty"`

	AudioFakeScore *float64 `json:"audio_fake_score,omitempty"`
	AudioVerdict   string   `json:"audio_verdict,omitempty"`

	TemporalScore *float64 `json:"temporal_score,omitempty"`

	RiskScore   float64  `json:"risk_score"`
	RiskVerdict string   `json:"risk_verdict"`
	RiskReasons []string `json:"risk_reasons"`

	CreatedAt time.Time `json:"created_at"`
}

// ComputeRisk recomputes a combined risk score/verdict/reasons from
// whatever video/audio/temporal scores are on record after an upsert —
// nil for any signal that's never completed. Supplied by the caller
// (see internal/analysisworker) rather than this package importing
// internal/risk directly, so the actual weighting/threshold logic for
// combining signals stays out of the persistence layer.
//
// May be nil when passed to UpsertVideoResult/UpsertAudioResult: a
// completion coordinator (7.6.6) can decide the other modality is
// still outstanding and the score should be recorded without
// calculating a (premature) risk yet — see those methods' doc comments.
type ComputeRisk func(videoScore, audioScore, temporalScore *float64) (riskScore float64, riskVerdict string, riskReasons []string)

// Repository persists analysis results. *Postgres is the only
// implementation; handlers depend on this interface instead, the same
// separation internal/repository/sessions uses.
type Repository interface {
	// Create records a completed session's full analysis in one shot —
	// used by the live WebSocket pipeline (internal/realtime), which
	// already has every signal it'll ever have by the time a session
	// ends. Since this is 1:1 with sessions (see the schema comment in
	// 0002_create_analysis_results.sql), calling it twice for the same
	// SessionID is a bug in the caller, not a case to silently handle.
	Create(ctx context.Context, result Result) error
	// GetBySessionID looks up the analysis for a session. Returns
	// ErrNotFound if none exists yet (the session hasn't completed) or
	// ever will (the session ID is unknown).
	GetBySessionID(ctx context.Context, sessionID string) (*Result, error)
	// UpsertVideoResult records (or updates) sessionID's video signal —
	// creating the row if this is the first modality to complete for
	// this session, or merging into an existing row an
	// UpsertAudioResult call already created — and recomputes the
	// combined risk via compute, atomically with respect to a
	// concurrent UpsertAudioResult for the same session. Used by
	// VideoWorker (Phase 7.5): the async job path, where video and
	// audio for one session are independent jobs that can complete in
	// either order or concurrently, unlike the WebSocket path's single
	// Create.
	//
	// Idempotent by construction: calling it again with the same
	// sessionID/score/verdict (e.g. a redelivered job re-executing
	// after an ack was lost) just overwrites the same values and
	// recomputes the same risk — never a second row, never corruption.
	UpsertVideoResult(ctx context.Context, sessionID string, videoScore float64, videoVerdict string, compute ComputeRisk) error
	// UpsertAudioResult is UpsertVideoResult's audio counterpart.
	UpsertAudioResult(ctx context.Context, sessionID string, audioScore float64, audioVerdict string, compute ComputeRisk) error
	// FinalizeRisk recomputes and stores the combined risk from
	// whatever's already on record for sessionID, without changing any
	// of the underlying signals. Used when a modality's job is
	// dead-lettered rather than completed (see
	// internal/analysisworker's OnDeadLetter): nothing else will ever
	// call compute for that modality again, so without this the session
	// would wait forever for a result that's never coming. Returns
	// ErrNotFound if no analysis row exists yet for sessionID.
	FinalizeRisk(ctx context.Context, sessionID string, compute ComputeRisk) error
}
